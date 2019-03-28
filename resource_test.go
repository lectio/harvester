package harvester

import (
	"fmt"
	"io"
	"log"
	"net/url"
	"os"
	"path"
	"strings"
	"testing"
	"text/template"

	"github.com/opentracing/opentracing-go"
	observe "github.com/shah/observe-go"

	"github.com/stretchr/testify/suite"
)

type ResourceSuite struct {
	suite.Suite
	observatory observe.Observatory
	ch          *ContentHarvester
	harvested   *HarvestedResources
	markdown    map[string]*strings.Builder
	serializer  HarvestedResourcesSerializer
	span        opentracing.Span
}

func (suite *ResourceSuite) SetupSuite() {
	_, set := os.LookupEnv("JAEGER_SERVICE_NAME")
	if !set {
		os.Setenv("JAEGER_SERVICE_NAME", "Lectio Harvester Test Suite")
	}

	observatory := observe.MakeObservatoryFromEnv()
	suite.observatory = observatory
	suite.span = observatory.StartTrace("ResourceSuite")
	suite.ch = MakeContentHarvester(suite.observatory, DefaultIgnoreURLsRegExList, DefaultCleanURLsRegExList, false)

	tmpl, tmplErr := template.ParseFiles("serialize.md.tmpl")
	if tmplErr != nil {
		log.Fatalf("can't initialize template: %v", tmplErr)
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

func (suite *ResourceSuite) TearDownSuite() {
	suite.span.Finish()
	suite.observatory.Close()
}

func (suite *ResourceSuite) harvestSingleURLFromMockTweet(text string, msgAndArgs0 string, msgAndArgs ...interface{}) *HarvestedResource {
	suite.harvested = suite.ch.HarvestResources(fmt.Sprintf(text, msgAndArgs0, msgAndArgs), suite.span)
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

func (suite *ResourceSuite) TestSimplifiedHostnames() {
	url, _ := url.Parse("https://www.netspective.com")
	suite.Equal("netspective.com", GetSimplifiedHostname(url))
	suite.Equal("netspective", GetSimplifiedHostnameWithoutTLD(url))
	url, _ = url.Parse("https://news.healthcareguys.com")
	suite.Equal("news.healthcareguys.com", GetSimplifiedHostname(url))
	suite.Equal("news.healthcareguys", GetSimplifiedHostnameWithoutTLD(url))
}

func (suite *ResourceSuite) TestOpenGraphMetaTags() {
	hr := suite.harvestSingleURLFromMockTweet("Test a good URL %s which will redirect to a URL we want to ignore, with utm_* params", "http://bit.ly/lectio_harvester_resource_test01")
	isURLValid, isDestValid := hr.IsValid()
	suite.True(isURLValid, "URL should be formatted validly")
	suite.True(isDestValid, "URL should have valid destination")
	suite.NotNil(hr.ResourceContent(), "Content should be available")

	content := hr.ResourceContent()
	value, _ := content.GetOpenGraphMetaTag("site_name")
	suite.Equal(value, "Netspective")

	value, _ = content.GetOpenGraphMetaTag("title")
	suite.Equal(value, "Safety, privacy, and security focused technology consulting")

	value, _ = content.GetOpenGraphMetaTag("description")
	suite.Equal(value, "Software, technology, and management consulting focused on firms im pacted by FDA, ONC, NIST or other safety, privacy, and security regulations")
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
	hr := suite.harvestSingleURLFromMockTweet("Test a good URL %s which will redirect to a URL we want to resolve via <meta http-equiv='refresh' content='delay;url='>, with utm_* params", "http://bit.ly/lectio_harvester_resource_test03")
	isURLValid, isDestValid := hr.IsValid()
	suite.True(isURLValid, "URL should be formatted validly")
	suite.True(isDestValid, "URL should have valid destination")
	isIgnored, _ := hr.IsIgnored()
	suite.False(isIgnored, "URL should not be ignored")
	isHTMLRedirect, htmlRedirectURLText := hr.IsHTMLRedirect()
	suite.True(isHTMLRedirect, "There should have been an HTML redirect requested through <meta http-equiv='refresh' content='delay;url='>")
	suite.Equal(htmlRedirectURLText, "https://www.netspective.com/?utm_source=lectio_harvester_resource_test.go&utm_medium=go.TestSuite&utm_campaign=harvester.ResourceSuite")
	suite.NotNil(hr.ResourceContent(), "Content should be available")

	// at this point we want to get the "new" (redirected) and test it
	span := suite.observatory.StartChildTrace("TestResolvedURLRedirectedThroughHTMLProperly", suite.span)
	defer span.Finish()
	redirectedHR := harvestResourceFromReferrer(suite.ch, span, hr)
	suite.Equal(redirectedHR.ReferredByResource(), hr, "The referral resource should be the same as the original")
	isURLValid, isDestValid = redirectedHR.IsValid()
	suite.True(isURLValid, "Redirected URL should be formatted validly")
	suite.True(isDestValid, "Redirected URL should have valid destination")
	isIgnored, _ = redirectedHR.IsIgnored()
	suite.False(isIgnored, "Redirected URL should not be ignored")
	isCleaned, _ := redirectedHR.IsCleaned()
	suite.True(isCleaned, "Redirected URL should be 'cleaned'")
	finalURL, resolvedURL, cleanedURL := redirectedHR.GetURLs()
	suite.Equal(resolvedURL.String(), "https://www.netspective.com/?utm_source=lectio_harvester_resource_test.go&utm_medium=go.TestSuite&utm_campaign=harvester.ResourceSuite")
	suite.Equal(cleanedURL.String(), "https://www.netspective.com/")
	suite.Equal(finalURL.String(), cleanedURL.String(), "finalURL should be same as cleanedURL")
	suite.NotNil(redirectedHR.ResourceContent(), "Content should be available")
}

func (suite *ResourceSuite) TestResolvedURLCleaned() {
	hr := suite.harvestSingleURLFromMockTweet("Test a good URL %s which will redirect to a URL we want to ignore, with utm_* params", "http://bit.ly/lectio_harvester_resource_test01")
	isURLValid, isDestValid := hr.IsValid()
	suite.True(isURLValid, "URL should be formatted validly")
	suite.True(isDestValid, "URL should have valid destination")
	isIgnored, _ := hr.IsIgnored()
	suite.False(isIgnored, "URL should not be ignored")
	isCleaned, _ := hr.IsCleaned()
	suite.True(isCleaned, "URL should be 'cleaned'")
	finalURL, resolvedURL, cleanedURL := hr.GetURLs()
	suite.Equal(resolvedURL.String(), "https://www.netspective.com/?utm_source=lectio_harvester_resource_test.go&utm_medium=go.TestSuite&utm_campaign=harvester.ResourceSuite")
	suite.Equal(cleanedURL.String(), "https://www.netspective.com/")
	suite.Equal(finalURL.String(), cleanedURL.String(), "finalURL should be same as cleanedURL")
	suite.NotNil(hr.ResourceContent(), "Content should be available")
}

func (suite *ResourceSuite) TestResolvedURLCleanedKeys() {
	hr := suite.harvestSingleURLFromMockTweet("Test a good URL %s which will redirect to a URL we want to ignore, with utm_* params", "http://bit.ly/lectio_harvester_resource_test02")
	isURLValid, isDestValid := hr.IsValid()
	suite.True(isURLValid, "URL should be formatted validly")
	suite.True(isDestValid, "URL should have valid destination")
	isIgnored, _ := hr.IsIgnored()
	suite.False(isIgnored, "URL should not be ignored")
	isCleaned, _ := hr.IsCleaned()
	suite.True(isCleaned, "URL should be 'cleaned'")
	finalURL, resolvedURL, cleanedURL := hr.GetURLs()
	suite.Equal(resolvedURL.String(), "https://www.netspective.com/solutions/opsfolio/?utm_source=lectio_harvester_resource_test.go&utm_medium=go.TestSuite&utm_campaign=harvester.ResourceSuite")
	suite.Equal(cleanedURL.String(), "https://www.netspective.com/solutions/opsfolio/")
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
	suite.Equal(keys.Slug(), "hipaa-compliant-cybersecurity-andamp-risk-assessment-software-netspective-opsfolio")
	suite.NotNil(hr.ResourceContent(), "Content should be available")
}

func (suite *ResourceSuite) TestResolvedURLCleanedSerializer() {
	hr := suite.harvestSingleURLFromMockTweet("Test a good URL %s which will redirect to a URL we want to ignore, with utm_* params", "http://bit.ly/lectio_harvester_resource_test02")
	isURLValid, isDestValid := hr.IsValid()
	suite.True(isURLValid, "URL should be formatted validly")
	suite.True(isDestValid, "URL should have valid destination")
	isIgnored, _ := hr.IsIgnored()
	suite.False(isIgnored, "URL should not be ignored")
	isCleaned, _ := hr.IsCleaned()
	suite.True(isCleaned, "URL should be 'cleaned'")
	finalURL, resolvedURL, cleanedURL := hr.GetURLs()
	suite.Equal(resolvedURL.String(), "https://www.netspective.com/solutions/opsfolio/?utm_source=lectio_harvester_resource_test.go&utm_medium=go.TestSuite&utm_campaign=harvester.ResourceSuite")
	suite.Equal(cleanedURL.String(), "https://www.netspective.com/solutions/opsfolio/")
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
	suite.Equal(resolvedURL.String(), "https://www.foxnews.com/lifestyle/photo-of-donald-trump-look-alike-in-spain-goes-viral")
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
	suite.Equal(content.downloaded.FileType.Extension, "pdf")

	fileExists := false
	if _, err := os.Stat(content.downloaded.DestPath); err == nil {
		fileExists = true
	}
	suite.True(fileExists, "File %s should exist", content.downloaded.DestPath)
	suite.Equal(path.Ext(content.downloaded.DestPath), ".pdf", "File's extension should be .pdf")
}

func TestSuite(t *testing.T) {
	suite.Run(t, new(ResourceSuite))
}
