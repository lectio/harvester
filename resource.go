package harvester

import (
	"fmt"
	"io"
	"io/ioutil"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"regexp"
	"strings"
	"time"

	filetype "github.com/h2non/filetype"
	"github.com/h2non/filetype/types"
	opentracing "github.com/opentracing/opentracing-go"
	opentrext "github.com/opentracing/opentracing-go/ext"
	"github.com/opentracing/opentracing-go/log"
	observe "github.com/shah/observe-go"
	"golang.org/x/net/html"
)

// DownloadedContent manages any content that was downloaded for further inspection
type DownloadedContent struct {
	URL           *url.URL
	DestPath      string
	DownloadError error
	FileTypeError error
	FileType      types.Type
}

// Delete removes the file that was downloaded
func (dc *DownloadedContent) Delete() {
	os.Remove(dc.DestPath)
}

// DownloadContent will download a url to a local file. It's efficient because it will
// write as it downloads and not load the whole file into memory.
func DownloadContent(url *url.URL, resp *http.Response, o observe.Observatory, parentSpan opentracing.Span) *DownloadedContent {
	span := o.StartChildTrace("DownloadContent", parentSpan)
	defer span.Finish()

	destFile, err := ioutil.TempFile(os.TempDir(), "harvester-dl-")
	span.LogFields(log.String("downloadedAsName", destFile.Name()))

	result := new(DownloadedContent)
	result.URL = url
	if err != nil {
		result.DownloadError = err
		opentrext.Error.Set(span, true)
		span.LogFields(log.Error(err))
		return result
	}

	defer destFile.Close()
	defer resp.Body.Close()
	result.DestPath = destFile.Name()
	_, err = io.Copy(destFile, resp.Body)
	if err != nil {
		result.DownloadError = err
		opentrext.Error.Set(span, true)
		span.LogFields(log.Error(err))
		return result
	}
	destFile.Close()

	// Open the just-downloaded file again since it was closed already
	file, err := os.Open(result.DestPath)
	if err != nil {
		result.FileTypeError = err
		opentrext.Error.Set(span, true)
		span.LogFields(log.Error(err))
		return result
	}

	// We only have to pass the file header = first 261 bytes
	head := make([]byte, 261)
	file.Read(head)
	file.Close()

	result.FileType, result.FileTypeError = filetype.Match(head)
	if result.FileTypeError == nil {
		// change the extension so that it matches the file type we found
		currentPath := result.DestPath
		currentExtension := path.Ext(currentPath)
		newPath := currentPath[0:len(currentPath)-len(currentExtension)] + "." + result.FileType.Extension
		os.Rename(currentPath, newPath)
		result.DestPath = newPath
		span.LogFields(log.String("FinalDestName", newPath))
	}

	return result
}

// HarvestedResourceContent manages the kind of content was inspected
type HarvestedResourceContent struct {
	url                          *url.URL
	contentType                  string
	mediaType                    string
	mediaTypeParams              map[string]string
	mediaTypeError               error
	htmlParseError               error
	isHTMLRedirect               bool
	metaRefreshTagContentURLText string            // if IsHTMLRedirect is true, then this is the value after url= in something like <meta http-equiv='refresh' content='delay;url='>
	metaPropertyTags             map[string]string // if IsHTML() is true, a collection of all meta data like <meta property="og:site_name" content="Netspective" /> or <meta name="twitter:title" content="text" />
	downloaded                   *DownloadedContent
}

// DetectHarvestedResourceContent will figure out what kind of destination content we're dealing with
func DetectHarvestedResourceContent(url *url.URL, resp *http.Response, o observe.Observatory, parentSpan opentracing.Span) *HarvestedResourceContent {
	result := new(HarvestedResourceContent)
	result.metaPropertyTags = make(map[string]string)
	result.url = url
	result.contentType = resp.Header.Get("Content-Type")
	if len(result.contentType) > 0 {
		result.mediaType, result.mediaTypeParams, result.mediaTypeError = mime.ParseMediaType(result.contentType)
		if result.mediaTypeError != nil {
			span := o.StartChildTrace("detectResourceContent", parentSpan)
			defer span.Finish()
			opentrext.Error.Set(span, true)
			span.LogFields(
				log.String("unknown ContentType", result.contentType),
				log.Error(result.mediaTypeError))
			return result
		}
		if result.IsHTML() {
			result.parsePageMetaData(url, resp, o, parentSpan)
			return result
		}
	}

	// If we get to here it means that we need to download the content to inspect it.
	// We download it first because it's possible we want to retain it for later use.
	result.downloaded = DownloadContent(url, resp, o, parentSpan)
	return result
}

// metaRefreshContentRegEx is used to match the 'content' attribute in a tag like this:
//   <meta http-equiv="refresh" content="2;url=https://www.google.com">
var metaRefreshContentRegEx = regexp.MustCompile(`^(\d?)\s?;\s?url=(.*)$`)

func (c *HarvestedResourceContent) parsePageMetaData(url *url.URL, resp *http.Response, o observe.Observatory, parentSpan opentracing.Span) error {
	span := o.StartChildTrace("getPageMetaData", parentSpan)
	defer span.Finish()

	doc, parseError := html.Parse(resp.Body)
	if parseError != nil {
		opentrext.Error.Set(span, true)
		span.LogFields(log.Error(parseError))
		c.htmlParseError = parseError
		return parseError
	}
	defer resp.Body.Close()

	var inHead bool
	var f func(*html.Node)
	f = func(n *html.Node) {
		if n.Type == html.ElementNode && strings.EqualFold(n.Data, "head") {
			inHead = true
		}
		if inHead && n.Type == html.ElementNode && strings.EqualFold(n.Data, "meta") {
			for _, attr := range n.Attr {
				if strings.EqualFold(attr.Key, "http-equiv") && strings.EqualFold(strings.TrimSpace(attr.Val), "refresh") {
					for _, attr := range n.Attr {
						if strings.EqualFold(attr.Key, "content") {
							contentValue := strings.TrimSpace(attr.Val)
							parts := metaRefreshContentRegEx.FindStringSubmatch(contentValue)
							if parts != nil && len(parts) == 3 {
								// the first part is the entire match
								// the second and third parts are the delay and URL
								// See for explanation: http://redirectdetective.com/redirection-types.html
								c.isHTMLRedirect = true
								c.metaRefreshTagContentURLText = parts[2]
							}
						}
					}
				}
				if strings.EqualFold(attr.Key, "property") || strings.EqualFold(attr.Key, "name") {
					propertyName := attr.Val
					for _, attr := range n.Attr {
						if strings.EqualFold(attr.Key, "content") {
							c.metaPropertyTags[propertyName] = attr.Val
						}
					}
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			f(c)
		}
	}
	f(doc)
	return nil
}

// IsValid returns true if this there are no errors
func (c HarvestedResourceContent) IsValid() bool {
	if c.mediaTypeError != nil {
		return false
	}

	if c.downloaded != nil {
		if c.downloaded.DownloadError != nil {
			return false
		}
		if c.downloaded.FileTypeError != nil {
			return false
		}
	}

	return true
}

// IsHTML returns true if this is HTML content
func (c HarvestedResourceContent) IsHTML() bool {
	return c.mediaType == "text/html"
}

// GetOpenGraphMetaTag returns the value and true if og:key was found
func (c HarvestedResourceContent) GetOpenGraphMetaTag(key string) (string, bool) {
	result, ok := c.metaPropertyTags["og:"+key]
	return result, ok
}

// GetTwitterMetaTag returns the value and true if og:key was found
func (c HarvestedResourceContent) GetTwitterMetaTag(key string) (string, bool) {
	result, ok := c.metaPropertyTags["twitter:"+key]
	return result, ok
}

// WasDownloaded returns true if content was downloaded for inspection
func (c HarvestedResourceContent) WasDownloaded() bool {
	return c.downloaded != nil
}

// IsHTMLRedirect returns true if redirect was requested through via <meta http-equiv='refresh' content='delay;url='>
// For an explanation, please see http://redirectdetective.com/redirection-types.html
func (c HarvestedResourceContent) IsHTMLRedirect() (bool, string) {
	return c.isHTMLRedirect, c.metaRefreshTagContentURLText
}

// HarvestedResource tracks a single URL that was discovered in content.
// Discovered URLs are validated, follow their redirects, and may have
// query parameters "cleaned" (if instructed).
type HarvestedResource struct {
	// TODO consider adding source information (e.g. tweet, e-mail, etc.) and embed style (e.g. text, HTML <a> tag, etc.)
	harvestedOn     time.Time
	origURLtext     string
	origResource    *HarvestedResource
	isURLValid      bool
	isDestValid     bool
	httpStatusCode  int
	isURLIgnored    bool
	ignoreReason    string
	isURLCleaned    bool
	isURLAttachment bool
	resolvedURL     *url.URL
	cleanedURL      *url.URL
	finalURL        *url.URL
	resourceContent *HarvestedResourceContent
}

// OriginalURLText returns the URL as it was discovered, with no alterations
func (r *HarvestedResource) OriginalURLText() string {
	return r.origURLtext
}

// ReferredByResource returns the original resource that referred this one,
// which is only non-nil when this resource was an HTML (not HTTP) redirect
func (r *HarvestedResource) ReferredByResource() *HarvestedResource {
	return r.origResource
}

// IsValid indicates whether (a) the original URL was parseable and (b) whether
// the destination is valid -- meaning not a 404 or something else
func (r *HarvestedResource) IsValid() (bool, bool) {
	return r.isURLValid, r.isDestValid
}

// IsIgnored indicates whether the URL should be ignored based on harvesting rules.
// Discovered URLs may be ignored for a variety of reasons using a list of Regexps.
func (r *HarvestedResource) IsIgnored() (bool, string) {
	return r.isURLIgnored, r.ignoreReason
}

// IsCleaned indicates whether URL query parameters were removed and the new "cleaned" URL
func (r *HarvestedResource) IsCleaned() (bool, *url.URL) {
	return r.isURLCleaned, r.cleanedURL
}

// GetURLs returns the final (most useful), originally resolved, and "cleaned" URLs
func (r *HarvestedResource) GetURLs() (*url.URL, *url.URL, *url.URL) {
	return r.finalURL, r.resolvedURL, r.cleanedURL
}

// IsHTMLRedirect returns true if redirect was requested through via <meta http-equiv='refresh' content='delay;url='>
// For an explanation, please see http://redirectdetective.com/redirection-types.html
func (r *HarvestedResource) IsHTMLRedirect() (bool, string) {
	content := r.resourceContent
	if content != nil {
		return content.IsHTMLRedirect()
	} else {
		return false, ""
	}
}

// ResourceContent returns the inspected or downloaded content
func (r *HarvestedResource) ResourceContent() *HarvestedResourceContent {
	return r.resourceContent
}

// cleanResource checks to see if there are any parameters that should be removed (e.g. UTM_*)
func cleanResource(url *url.URL, rule CleanDiscoveredResourceRule, o observe.Observatory, parentSpan opentracing.Span) (bool, *url.URL) {
	span := o.StartChildTrace("cleanResource", parentSpan)
	defer span.Finish()

	if !rule.CleanDiscoveredResource(url) {
		return false, nil
	}

	// make a copy because we're planning on changing the URL params
	cleanedURL, error := url.Parse(url.String())
	if error != nil {
		opentrext.Error.Set(span, true)
		span.LogFields(log.Error(error))
		return false, nil
	}

	harvestedParams := cleanedURL.Query()
	type ParamMatch struct {
		paramName string
		reason    string
	}
	var cleanedParams []ParamMatch
	for paramName := range harvestedParams {
		remove, reason := rule.RemoveQueryParamFromResource(paramName)
		if remove {
			harvestedParams.Del(paramName)
			cleanedParams = append(cleanedParams, ParamMatch{paramName, reason})
		}
	}

	if len(cleanedParams) > 0 {
		cleanedURL.RawQuery = harvestedParams.Encode()
		return true, cleanedURL
	}
	return false, nil
}

func harvestResource(h *ContentHarvester, parentSpan opentracing.Span, origURLtext string) *HarvestedResource {
	span := h.observatory.StartChildTrace("harvestResource", parentSpan)
	defer span.Finish()
	span.LogFields(log.String("origURLtext", origURLtext))

	result := new(HarvestedResource)
	result.origURLtext = origURLtext
	result.harvestedOn = time.Now()

	// Use the standard Go HTTP library method to retrieve the content; the
	// default will automatically follow redirects (e.g. HTTP redirects)
	resp, err := http.Get(origURLtext)
	result.isURLValid = err == nil
	if result.isURLValid == false {
		result.isDestValid = false
		result.isURLIgnored = true
		result.ignoreReason = fmt.Sprintf("Invalid URL '%s'", origURLtext)
		span.LogFields(
			log.Bool("isDestValid", result.isDestValid),
			log.Bool("isURLIgnored", result.isURLIgnored),
			log.String("ignoreReason", result.ignoreReason),
			log.Error(err),
		)
		opentrext.Error.Set(span, true)
		return result
	}

	result.httpStatusCode = resp.StatusCode
	if result.httpStatusCode != 200 {
		result.isDestValid = false
		result.isURLIgnored = true
		result.ignoreReason = fmt.Sprintf("Invalid HTTP Status Code %d", resp.StatusCode)
		span.LogFields(
			log.Bool("isDestValid", result.isDestValid),
			log.Bool("isURLIgnored", result.isURLIgnored),
			log.String("ignoreReason", result.ignoreReason),
		)
		opentrext.Error.Set(span, true)
		opentrext.HTTPStatusCode.Set(span, uint16(result.httpStatusCode))
		return result
	}

	result.resolvedURL = resp.Request.URL
	result.finalURL = result.resolvedURL
	ignoreURL, ignoreReason := h.ignoreResourceRule.IgnoreDiscoveredResource(result.resolvedURL)
	if ignoreURL {
		result.isDestValid = true
		result.isURLIgnored = true
		result.ignoreReason = ignoreReason
		span.LogFields(
			log.Bool("isDestValid", result.isDestValid),
			log.Bool("isURLIgnored", result.isURLIgnored),
			log.String("ignoreReason", result.ignoreReason),
		)
		return result
	}

	result.isURLIgnored = false
	result.isDestValid = true
	urlsParamsCleaned, cleanedURL := cleanResource(result.resolvedURL, h.cleanResourceRule, h.observatory, span)
	if urlsParamsCleaned {
		result.cleanedURL = cleanedURL
		result.finalURL = cleanedURL
		result.isURLCleaned = true
	} else {
		result.isURLCleaned = false
	}

	result.resourceContent = h.detectResourceContent(result.finalURL, resp, h.observatory, span)
	span.LogFields(log.Object("result", result))

	// TODO once the URL is cleaned, double-check the cleaned URL to see if it's a valid destination; if not, revert to non-cleaned version
	// this could be done recursively here or by the outer function. This is necessary because "cleaning" a URL and removing params might
	// break it so we need to revert to original.

	return result
}

func harvestResourceFromReferrer(h *ContentHarvester, parentSpan opentracing.Span, original *HarvestedResource) *HarvestedResource {
	isHTMLRedirect, htmlRedirectURL := original.IsHTMLRedirect()
	if !isHTMLRedirect {
		return nil
	}
	span := h.observatory.StartChildTrace("harvestResourceFromReferrer", parentSpan)
	defer span.Finish()
	span.LogFields(log.Object("original", *original),
		log.Bool("isHTMLRedirect", isHTMLRedirect),
		log.String("htmlRedirectURL", htmlRedirectURL))

	result := harvestResource(h, span, htmlRedirectURL)
	result.origResource = original
	return result
}
