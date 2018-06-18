package harvester

import (
	"fmt"
	"io"
	"log"
	"os"
	"path"
	"strings"
	"testing"
	"text/template"

	"github.com/stretchr/testify/suite"
	"go.uber.org/zap"
)

type ResourceSuite struct {
	suite.Suite
	logger     *zap.Logger
	ch         *ContentHarvester
	harvested  *HarvestedResources
	markdown   map[string]*strings.Builder
	serializer HarvestedResourcesSerializer
}

func (suite *ResourceSuite) SetupSuite() {
	logger, err := zap.NewProduction()
	if err != nil {
		log.Fatalf("can't initialize zap logger: %v", err)
	}
	suite.logger = logger
	suite.ch = MakeContentHarvester(suite.logger, defaultIgnoreURLsRegExList, defaultCleanURLsRegExList, false)

	tmpl, tmplErr := template.ParseFiles("serialize.md.tmpl")
	if tmplErr != nil {
		log.Fatalf("can't initialize template: %v", err)
	}
	suite.markdown = make(map[string]*strings.Builder)
	suite.serializer = HarvestedResourcesSerializer{
		GetKeys: func(hr *HarvestedResource) *HarvestedResourceKeys {
			return CreateHarvestedResourceKeys(hr, func(random uint32, try int) bool {
				return false
			})
		},
		GetTemplate: func(keys *HarvestedResourceKeys) (*template.Template, error) {
			return tmpl, nil
		},
		GetTemplateParams: func(keys *HarvestedResourceKeys) *map[string]interface{} {
			result := make(map[string]interface{})
			result["ProvenanceType"] = "tweet"
			return &result
		},
		GetWriter: func(keys *HarvestedResourceKeys) io.Writer {
			markdown, found := suite.markdown[keys.hr.finalURL.String()]
			if !found {
				markdown = new(strings.Builder)
				suite.markdown[keys.hr.finalURL.String()] = markdown
			}
			return markdown
		},
		HandleInvalidURL:     nil,
		HandleInvalidURLDest: nil,
		HandleIgnoredURL:     nil,
	}
}

func (suite *ResourceSuite) harvestSingleURLFromMockTweet(text string, msgAndArgs ...interface{}) *HarvestedResource {
	suite.harvested = suite.ch.HarvestResources(fmt.Sprintf(text, msgAndArgs))
	suite.Equal(len(suite.harvested.Resources), 1)
	return suite.harvested.Resources[0]
}

func (suite *ResourceSuite) TestInvalidlyFormattedURLs() {
	hr := suite.harvestSingleURLFromMockTweet("Test an invalidly formatted URL %s in a mock tweet", "https://t")
	isURLValid, isDestValid := hr.IsValid()
	suite.False(isURLValid, "URL should have invalid format")
	suite.False(isDestValid, "URL should have invalid destination")
	suite.Nil(hr.ResourceContent(), "No content should be available")
}

func (suite *ResourceSuite) TestInvalidDestinationURLs() {
	hr := suite.harvestSingleURLFromMockTweet("Test a validly formatted URL %s but with invalid destination in a mock tweet", "https://t.co/fDxPF")
	isURLValid, isDestValid := hr.IsValid()
	suite.True(isURLValid, "URL should be formatted validly")
	suite.False(isDestValid, "URL should have invalid destination")
	suite.Equal(hr.httpStatusCode, 404)
	suite.Nil(hr.ResourceContent(), "No content should be available")
}

func (suite *ResourceSuite) TestIgnoreRules() {
	hr := suite.harvestSingleURLFromMockTweet("Test a good URL %s which will redirect to a URL we want to ignore", "https://t.co/xNzrxkHE1u")
	isURLValid, isDestValid := hr.IsValid()
	suite.True(isURLValid, "URL should be formatted validly")
	suite.True(isDestValid, "URL should have valid destination")
	isIgnored, ignoreReason := hr.IsIgnored()
	suite.True(isIgnored, "URL should be ignored (skipped)")
	suite.Equal(ignoreReason, "Matched Ignore Rule `^https://twitter.com/(.*?)/status/(.*)$`")
	suite.Nil(hr.ResourceContent(), "No content should be available")
}

func (suite *ResourceSuite) TestResolvedURLRedirectedThroughHTMLProperly() {
	hr := suite.harvestSingleURLFromMockTweet("Test a good URL %s which will redirect to a URL we want to resolve via <meta http-equiv='refresh' content='delay;url='>, with utm_* params", "https://t.co/4dcdNEQYHa")
	isURLValid, isDestValid := hr.IsValid()
	suite.True(isURLValid, "URL should be formatted validly")
	suite.True(isDestValid, "URL should have valid destination")
	isIgnored, _ := hr.IsIgnored()
	suite.False(isIgnored, "URL should not be ignored")
	isHTMLRedirect, htmlRedirectURLText := hr.IsHTMLRedirect()
	suite.True(isHTMLRedirect, "There should have been an HTML redirect requested through <meta http-equiv='refresh' content='delay;url='>")
	suite.Equal(htmlRedirectURLText, "https://www.sopranodesign.com/secure-healthcare-messaging/?utm_source=twitter&utm_medium=socialmedia&utm_campaign=soprano")
	suite.NotNil(hr.ResourceContent(), "Content should be available")

	// at this point we want to get the "new" (redirected) and test it
	redirectedHR := harvestResourceFromReferrer(suite.ch, hr)
	suite.Equal(redirectedHR.ReferredByResource(), hr, "The referral resource should be the same as the original")
	isURLValid, isDestValid = redirectedHR.IsValid()
	suite.True(isURLValid, "Redirected URL should be formatted validly")
	suite.True(isDestValid, "Redirected URL should have valid destination")
	isIgnored, _ = redirectedHR.IsIgnored()
	suite.False(isIgnored, "Redirected URL should not be ignored")
	isCleaned, _ := redirectedHR.IsCleaned()
	suite.True(isCleaned, "Redirected URL should be 'cleaned'")
	finalURL, resolvedURL, cleanedURL := redirectedHR.GetURLs()
	suite.Equal(resolvedURL.String(), "https://www.sopranodesign.com/secure-healthcare-messaging/?utm_source=twitter&utm_medium=socialmedia&utm_campaign=soprano")
	suite.Equal(cleanedURL.String(), "https://www.sopranodesign.com/secure-healthcare-messaging/")
	suite.Equal(finalURL.String(), cleanedURL.String(), "finalURL should be same as cleanedURL")
	suite.NotNil(redirectedHR.ResourceContent(), "Content should be available")
}

func (suite *ResourceSuite) TestResolvedURLCleaned() {
	hr := suite.harvestSingleURLFromMockTweet("Test a good URL %s which will redirect to a URL we want to ignore, with utm_* params", "https://t.co/csWpQq5mbn")
	isURLValid, isDestValid := hr.IsValid()
	suite.True(isURLValid, "URL should be formatted validly")
	suite.True(isDestValid, "URL should have valid destination")
	isIgnored, _ := hr.IsIgnored()
	suite.False(isIgnored, "URL should not be ignored")
	isCleaned, _ := hr.IsCleaned()
	suite.True(isCleaned, "URL should be 'cleaned'")
	finalURL, resolvedURL, cleanedURL := hr.GetURLs()
	suite.Equal(resolvedURL.String(), "https://www.washingtonexaminer.com/chris-matthews-trump-russia-collusion-theory-came-apart-with-comey-testimony/article/2625372?utm_campaign=crowdfire&utm_content=crowdfire&utm_medium=social&utm_source=twitter")
	suite.Equal(cleanedURL.String(), "https://www.washingtonexaminer.com/chris-matthews-trump-russia-collusion-theory-came-apart-with-comey-testimony/article/2625372")
	suite.Equal(finalURL.String(), cleanedURL.String(), "finalURL should be same as cleanedURL")
	suite.NotNil(hr.ResourceContent(), "Content should be available")
}

func (suite *ResourceSuite) TestResolvedURLCleanedKeys() {
	hr := suite.harvestSingleURLFromMockTweet("Test a good URL %s which will redirect to a URL we want to ignore, with utm_* params", "https://t.co/csWpQq5mbn")
	isURLValid, isDestValid := hr.IsValid()
	suite.True(isURLValid, "URL should be formatted validly")
	suite.True(isDestValid, "URL should have valid destination")
	isIgnored, _ := hr.IsIgnored()
	suite.False(isIgnored, "URL should not be ignored")
	isCleaned, _ := hr.IsCleaned()
	suite.True(isCleaned, "URL should be 'cleaned'")
	finalURL, resolvedURL, cleanedURL := hr.GetURLs()
	suite.Equal(resolvedURL.String(), "https://www.washingtonexaminer.com/chris-matthews-trump-russia-collusion-theory-came-apart-with-comey-testimony/article/2625372?utm_campaign=crowdfire&utm_content=crowdfire&utm_medium=social&utm_source=twitter")
	suite.Equal(cleanedURL.String(), "https://www.washingtonexaminer.com/chris-matthews-trump-russia-collusion-theory-came-apart-with-comey-testimony/article/2625372")
	suite.Equal(finalURL.String(), cleanedURL.String(), "finalURL should be same as cleanedURL")

	var testRandom uint32
	var testTry int
	keys := CreateHarvestedResourceKeys(hr, func(random uint32, try int) bool {
		testRandom = random
		testTry = try
		return false
	})
	suite.Equal(testTry, 0)
	suite.Equal(keys.UniqueID(), testRandom)
	suite.Equal(keys.Slug(), "chris-matthews-trump-russia-collusion-theory-came-apart-with-comey-testimony")
	suite.NotNil(hr.ResourceContent(), "Content should be available")
}

func (suite *ResourceSuite) TestResolvedURLCleanedSerializer() {
	hr := suite.harvestSingleURLFromMockTweet("Test a good URL %s which will redirect to a URL we want to ignore, with utm_* params", "https://t.co/csWpQq5mbn")
	isURLValid, isDestValid := hr.IsValid()
	suite.True(isURLValid, "URL should be formatted validly")
	suite.True(isDestValid, "URL should have valid destination")
	isIgnored, _ := hr.IsIgnored()
	suite.False(isIgnored, "URL should not be ignored")
	isCleaned, _ := hr.IsCleaned()
	suite.True(isCleaned, "URL should be 'cleaned'")
	finalURL, resolvedURL, cleanedURL := hr.GetURLs()
	suite.Equal(resolvedURL.String(), "https://www.washingtonexaminer.com/chris-matthews-trump-russia-collusion-theory-came-apart-with-comey-testimony/article/2625372?utm_campaign=crowdfire&utm_content=crowdfire&utm_medium=social&utm_source=twitter")
	suite.Equal(cleanedURL.String(), "https://www.washingtonexaminer.com/chris-matthews-trump-russia-collusion-theory-came-apart-with-comey-testimony/article/2625372")
	suite.Equal(finalURL.String(), cleanedURL.String(), "finalURL should be same as cleanedURL")

	suite.NotNil(hr.ResourceContent(), "Content should be available")

	err := suite.harvested.Serialize(suite.serializer)
	suite.NoError(err, "Serialization should have occurred without error")

	_, found := suite.markdown[finalURL.String()]
	suite.True(found, "Markdown should have been serialized")
}

func (suite *ResourceSuite) TestResolvedURLNotCleaned() {
	hr := suite.harvestSingleURLFromMockTweet("Test a good URL %s which will redirect to a URL we want to ignore", "https://t.co/ELrZmo81wI")
	isURLValid, isDestValid := hr.IsValid()
	suite.True(isURLValid, "URL should be formatted validly")
	suite.True(isDestValid, "URL should have valid destination")
	isIgnored, _ := hr.IsIgnored()
	suite.False(isIgnored, "URL should not be ignored")
	isCleaned, _ := hr.IsCleaned()
	suite.False(isCleaned, "URL should not have been 'cleaned'")
	finalURL, resolvedURL, cleanedURL := hr.GetURLs()
	suite.Equal(resolvedURL.String(), "http://www.foxnews.com/lifestyle/2018/04/25/photo-donald-trump-look-alike-in-spain-goes-viral.html")
	suite.Equal(finalURL.String(), resolvedURL.String(), "finalURL should be same as resolvedURL")
	suite.Nil(cleanedURL, "cleanedURL should be empty")

	content := hr.ResourceContent()
	suite.NotNil(content, "The destination content should be available")
	suite.True(content.IsValid(), "The destination content should be valid")
	suite.True(content.IsHTML(), "The destination content should be HTML")
	suite.False(content.WasDownloaded(), "Because the destination was HTML, it should not have required to be downloaded")
}

func (suite *ResourceSuite) TestResolvedDocumentURLNotCleaned() {
	hr := suite.harvestSingleURLFromMockTweet("Check out the PROV-O specification document %s, which should resolve to an 'attachment' style URL", "http://ceur-ws.org/Vol-1401/paper-05.pdf")
	isURLValid, isDestValid := hr.IsValid()
	suite.True(isURLValid, "URL should be formatted validly")
	suite.True(isDestValid, "URL should have valid destination")
	isIgnored, _ := hr.IsIgnored()
	suite.False(isIgnored, "URL should not be ignored")
	isCleaned, _ := hr.IsCleaned()
	suite.False(isCleaned, "URL should not have been 'cleaned'")
	finalURL, resolvedURL, cleanedURL := hr.GetURLs()
	suite.Equal(resolvedURL.String(), "http://ceur-ws.org/Vol-1401/paper-05.pdf")
	suite.Equal(finalURL.String(), resolvedURL.String(), "finalURL should be same as resolvedURL")
	suite.Nil(cleanedURL, "cleanedURL should be empty")

	content := hr.ResourceContent()
	suite.NotNil(content, "The destination content should be available")
	suite.True(content.IsValid(), "The destination content should be valid")
	suite.True(content.WasDownloaded(), "Because the destination wasn't HTML, it should have been downloaded")
	suite.Equal(content.Downloaded.FileType.Extension, "pdf")

	fileExists := false
	if _, err := os.Stat(content.Downloaded.DestPath); err == nil {
		fileExists = true
	}
	suite.True(fileExists, "File %s should exist", content.Downloaded.DestPath)
	suite.Equal(path.Ext(content.Downloaded.DestPath), ".pdf", "File's extension should be .pdf")
}

func TestSuite(t *testing.T) {
	suite.Run(t, new(ResourceSuite))
}
