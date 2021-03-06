package harvester

import (
	"io"
	"net/http"
	"net/url"
	"regexp"
	"text/template"
	"time"

	"github.com/lectio/observe"
	"github.com/opentracing/opentracing-go"

	"github.com/opentracing/opentracing-go/log"
	"mvdan.cc/xurls"
)

// TODO use https://github.com/PuerkitoBio/goquery for parsing singe page HTML (similar to cheerio library for Node.js)
// TODO use https://github.com/gocolly/colly for scraping multiple page HTML sites
// TODO use https://github.com/andrewstuart/goq for type-safe layer on top of goquery using struct-tag
// TODO use https://medium.com/@aschers/deploy-machine-learning-models-from-r-research-to-ruby-go-production-with-pmml-b41e79445d3d for scoring content

// IgnoreDiscoveredResourceRule is a rule
type IgnoreDiscoveredResourceRule interface {
	IgnoreDiscoveredResource(url *url.URL) (bool, string)
}

// CleanDiscoveredResourceRule is a rule
type CleanDiscoveredResourceRule interface {
	CleanDiscoveredResource(url *url.URL) bool
	RemoveQueryParamFromResource(paramName string) (bool, string)
}

// ContentHarvester discovers URLs (called "Resources" from the "R" in "URL")
type ContentHarvester struct {
	observatory         observe.Observatory
	discoverURLsRegEx   *regexp.Regexp
	followHTMLRedirects bool
	ignoreResourceRule  IgnoreDiscoveredResourceRule
	cleanResourceRule   CleanDiscoveredResourceRule
	contentEncountered  []*HarvestedResourceContent
}

// HarvestedResources is the list of URLs discovered in a piece of content
type HarvestedResources struct {
	Content   string
	Resources []*HarvestedResource
}

// HarvestedResourcesSerializer contains callbacks for custom serialization of resources and content
type HarvestedResourcesSerializer struct {
	GetKeys              func(*HarvestedResource) *HarvestedResourceKeys
	GetTemplate          func(*HarvestedResourceKeys) (*template.Template, error)
	GetTemplateParams    func(*HarvestedResourceKeys) *map[string]interface{}
	GetWriter            func(*HarvestedResourceKeys) io.Writer
	HandleInvalidURL     func(*HarvestedResource)
	HandleInvalidURLDest func(*HarvestedResource)
	HandleIgnoredURL     func(*HarvestedResource)
}

// Serialize writes harvested content out to a storage device
func (r *HarvestedResources) Serialize(serializer HarvestedResourcesSerializer) error {
	for _, hr := range r.Resources {
		isURLValid, isDestValid := hr.IsValid()
		if !isURLValid {
			if serializer.HandleInvalidURL != nil {
				serializer.HandleInvalidURL(hr)
			}
			continue
		}
		if !isDestValid {
			if serializer.HandleInvalidURLDest != nil {
				serializer.HandleInvalidURLDest(hr)
			}
			continue
		}

		isIgnored, _ := hr.IsIgnored()
		if isIgnored {
			if serializer.HandleIgnoredURL != nil {
				serializer.HandleIgnoredURL(hr)
			}
			continue
		}

		keys := serializer.GetKeys(hr)
		t, tmplErr := serializer.GetTemplate(keys)
		if tmplErr != nil {
			return tmplErr
		}
		params := serializer.GetTemplateParams(keys)
		writer := serializer.GetWriter(keys)

		isCleaned, _ := hr.IsCleaned()
		finalURL, resolvedURL, _ := hr.GetURLs()
		err := t.Execute(writer, struct {
			Content     string
			Resource    *HarvestedResource
			HarvestedOn time.Time
			IsCleaned   bool
			FinalURL    string
			ResolvedURL string
			Params      *map[string]interface{}
			Slug        string
		}{
			r.Content,
			hr,
			hr.harvestedOn,
			isCleaned,
			finalURL.String(),
			resolvedURL.String(),
			params,
			keys.Slug(),
		})
		if err != nil {
			return err
		}
	}

	return nil
}

// MakeContentHarvester prepares a content harvester
func MakeContentHarvester(observatory observe.Observatory, ignoreResourceRule IgnoreDiscoveredResourceRule, cleanResourceRule CleanDiscoveredResourceRule, followHTMLRedirects bool) *ContentHarvester {
	result := new(ContentHarvester)
	result.observatory = observatory
	result.discoverURLsRegEx = xurls.Relaxed
	result.ignoreResourceRule = ignoreResourceRule
	result.cleanResourceRule = cleanResourceRule
	result.followHTMLRedirects = followHTMLRedirects
	return result
}

// MakeDefaultContentHarvester prepares a content harvester with sensible defaults
func MakeDefaultContentHarvester(observatory observe.Observatory) *ContentHarvester {
	return MakeContentHarvester(observatory, defaultIgnoreURLsRegExList, defaultCleanURLsRegExList, true)
}

// Close will clean up resources, mainly temporary files that were created for downloaded resources
func (h *ContentHarvester) Close() {

}

// detectContentType will figure out what kind of destination content we're dealing with
func (h *ContentHarvester) detectResourceContent(url *url.URL, resp *http.Response, o observe.Observatory, parentSpan opentracing.Span) *HarvestedResourceContent {
	result := DetectHarvestedResourceContent(url, resp, o, parentSpan)
	h.contentEncountered = append(h.contentEncountered, result)
	return result
}

// HarvestResources discovers URLs within content and returns what was found
func (h *ContentHarvester) HarvestResources(content string, parentSpan opentracing.Span) *HarvestedResources {
	span := h.observatory.StartChildTrace("HarvestResources", parentSpan)
	defer span.Finish()
	span.LogFields(log.String("content", content))

	result := new(HarvestedResources)
	result.Content = content

	seenUrls := make(map[string]bool)
	urls := h.discoverURLsRegEx.FindAllString(content, -1)
	for _, urlText := range urls {
		_, found := seenUrls[urlText]
		if found {
			continue
		}

		res := harvestResource(h, span, urlText)
		// check and see if we have an HTML content-based redirect via meta refresh (not HTTP)
		referredTo := harvestResourceFromReferrer(h, span, res)
		if referredTo != nil && h.followHTMLRedirects {
			// if we had a redirect, then that's the one we'll use
			res = referredTo
		}

		result.Resources = append(result.Resources, res)
		seenUrls[urlText] = true
	}
	return result
}
