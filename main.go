package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
)

// ExtractRequest represents the incoming request to extract media URL
type ExtractRequest struct {
	URL string `json:"url"`
}

// ExtractResponse represents the response with extracted media information
type ExtractResponse struct {
	MediaURL  string            `json:"mediaUrl"`
	MediaType string            `json:"mediaType"`
	Success   bool              `json:"success"`
	VideoID   string            `json:"videoId,omitempty"`
	Title     string            `json:"title,omitempty"`
	Duration  int64             `json:"duration,omitempty"`
	VideoUrls map[string]string `json:"videoUrls,omitempty"` // Multiple video quality URLs
	Metadata  map[string]string `json:"metadata,omitempty"`  // Additional extracted metadata
}

// ErrorResponse represents an error response
type ErrorResponse struct {
	Error   string `json:"error"`
	Success bool   `json:"success"`
}

// ThreadsExtractor handles the extraction logic
type ThreadsExtractor struct {
	browser *rod.Browser
}

// NewThreadsExtractor creates a new extractor instance
func NewThreadsExtractor() (*ThreadsExtractor, error) {
	// Configure launcher with optimized settings for faster performance
	launcher := launcher.New().
		Headless(true).
		NoSandbox(true).
		Devtools(false).
		Set("disable-gpu").
		Set("disable-dev-shm-usage").
		Set("disable-background-timer-throttling").
		Set("disable-backgrounding-occluded-windows").
		Set("disable-renderer-backgrounding").
		Set("disable-features", "TranslateUI").
		Set("disable-ipc-flooding-protection")

	// Try to find Chrome/Chromium automatically for Windows
	chromePath := os.Getenv("CHROME_PATH")
	if chromePath == "" {
		// Common Chrome paths on Windows
		commonPaths := []string{
			"C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe",
			"C:\\Program Files (x86)\\Google\\Chrome\\Application\\chrome.exe",
			"/usr/bin/google-chrome",
			"/usr/bin/chromium",
			"/usr/bin/chromium-browser",
		}

		for _, path := range commonPaths {
			if _, err := os.Stat(path); err == nil {
				chromePath = path
				break
			}
		}
	}

	// Set Chrome path if found
	if chromePath != "" {
		launcher = launcher.Bin(chromePath)
	}

	// Add proxy support if environment variable is set
	if proxy := os.Getenv("HTTP_PROXY"); proxy != "" {
		launcher = launcher.Proxy(proxy)
	}

	// Launch browser with error handling
	url, err := launcher.Launch()
	if err != nil {
		return nil, fmt.Errorf("failed to launch browser: %v", err)
	}

	browser := rod.New().
		ControlURL(url).
		MustConnect()

	return &ThreadsExtractor{
		browser: browser,
	}, nil
}

// Close cleans up the browser instance
func (te *ThreadsExtractor) Close() {
	if te.browser != nil {
		te.browser.MustClose()
	}
}

// normalizeURL removes query parameters and normalizes the Threads URL
func (te *ThreadsExtractor) normalizeURL(inputURL string) (string, error) {
	// Parse the URL
	parsedURL, err := url.Parse(inputURL)
	if err != nil {
		return "", fmt.Errorf("invalid URL format: %v", err)
	}

	// Check if it's a valid Threads URL (both .com and .net domains)
	validDomains := []string{"threads.com", "www.threads.com", "m.threads.com", "threads.net", "www.threads.net", "m.threads.net"}
	isValidDomain := false
	for _, domain := range validDomains {
		if strings.Contains(parsedURL.Host, domain) {
			isValidDomain = true
			break
		}
	}

	if !isValidDomain {
		return "", fmt.Errorf("URL must be from threads.com or threads.net")
	}

	// Validate URL pattern for Threads posts - example: /@username/post/ABC123DEF
	threadsPattern := regexp.MustCompile(`^/@[\w.-]+/post/[\w-]+/?`)
	if !threadsPattern.MatchString(parsedURL.Path) {
		return "", fmt.Errorf("invalid Threads post URL format - must be a post (/@username/post/POST_ID)")
	}

	// Normalize to threads.net and remove query parameters for cleaner URLs
	normalizedURL := fmt.Sprintf("https://www.threads.net%s", parsedURL.Path)

	return normalizedURL, nil
}

// extractMediaURL extracts the direct media URL from a Threads post
func (te *ThreadsExtractor) extractMediaURL(threadsURL string) (result *ExtractResponse, err error) {
	// Add panic recovery
	defer func() {
		if r := recover(); r != nil {
			log.Printf("Panic in extractMediaURL: %v", r)
			err = fmt.Errorf("extraction failed due to internal error")
		}
	}()

	// Normalize the URL
	normalizedURL, err := te.normalizeURL(threadsURL)
	if err != nil {
		return nil, err
	}

	// Create a new page
	page := te.browser.MustPage()
	defer page.MustClose()

	// Set user agent to avoid bot detection - use realistic desktop browser
	userAgent := "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"
	page.MustSetUserAgent(&proto.NetworkSetUserAgentOverride{
		UserAgent: userAgent,
	})

	// Set desktop viewport for better compatibility
	page.MustSetViewport(1920, 1080, 1, false)

	// Note: Rod has limited header support, focusing on user agent for bot detection avoidance

	// Navigation with faster timeout for speed
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	log.Printf("Fast navigation to: %s", normalizedURL)
	err = page.Context(ctx).Navigate(normalizedURL)
	if err != nil {
		log.Printf("Navigation error: %v", err)
		return nil, fmt.Errorf("failed to navigate to Threads post: %v", err)
	}

	// Wait for page load with timeout handling
	err = page.Context(ctx).WaitLoad()
	if err != nil {
		log.Printf("Page load timeout, proceeding anyway: %v", err)
	}

	// Quick wait for essential JavaScript content to load
	log.Printf("Quick wait for JavaScript content...")
	time.Sleep(1 * time.Second)

	// Wait for video elements with timeout
	log.Printf("Looking for video elements...")
	videoSelector := "video, [data-testid*='video'], [role='video'], video[src]"
	err = page.Context(ctx).WaitElementsMoreThan(videoSelector, 0)
	if err != nil {
		log.Printf("No video elements found immediately, proceeding: %v", err)
	} else {
		// Quick additional wait only if elements found
		time.Sleep(500 * time.Millisecond)
	}

	// Prioritize DOM elements extraction for JavaScript-rendered content
	log.Printf("Using DOM-based extraction for JavaScript-rendered Threads content")

	if result := te.extractFromDOMElements(page, "video"); result != nil {
		log.Printf("DOM extraction successful: %s (%s)", result.MediaType, result.MediaURL)
		return result, nil
	}

	// Fallback to enhanced source code search
	log.Printf("DOM extraction failed, trying enhanced source code analysis")
	if result := te.extractFromSourceCode(page, "video"); result != nil {
		log.Printf("Source code extraction successful: %s (%s)", result.MediaType, result.MediaURL)
		return result, nil
	}

	return nil, fmt.Errorf("Threads extraction failed - unable to find media URLs in page source")
}

// analyzePageContent determines if the page contains video or image content
func (te *ThreadsExtractor) analyzePageContent(page *rod.Page) string {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	// Check for comprehensive video indicators
	videoIndicators := []string{
		`meta[property="og:video:url"]`,
		`meta[property="og:video"]`,
		`meta[property="og:video:secure_url"]`,
		`meta[name="twitter:player:stream"]`,
		`meta[property="og:type"][content="video"]`,
		`meta[property="og:type"][content="video.other"]`,
	}

	for _, selector := range videoIndicators {
		if meta, err := page.Context(ctx).Element(selector); err == nil {
			if content, err := meta.Attribute("content"); err == nil && content != nil && *content != "" {
				log.Printf("Found video indicator: %s = %s", selector, *content)
				if strings.Contains(*content, "video") || strings.Contains(*content, ".mp4") {
					log.Printf("Detected video via meta tag: %s", selector)
					return "video"
				}
			}
		}
	}

	// Check for video elements with src or data attributes
	if videoElements, err := page.Context(ctx).Elements("video"); err == nil {
		for _, video := range videoElements {
			// Check src attribute
			if src, err := video.Attribute("src"); err == nil && src != nil && *src != "" {
				log.Printf("Found video element with src: %s", *src)
				if te.isValidVideoURL(*src) {
					log.Printf("Detected video via video element src")
					return "video"
				}
			}

			// Check data-src attribute
			if dataSrc, err := video.Attribute("data-src"); err == nil && dataSrc != nil && *dataSrc != "" {
				log.Printf("Found video element with data-src: %s", *dataSrc)
				if te.isValidVideoURL(*dataSrc) {
					log.Printf("Detected video via video element data-src")
					return "video"
				}
			}

			// Check for source elements within video
			if sources, err := video.Elements("source"); err == nil {
				for _, source := range sources {
					if src, err := source.Attribute("src"); err == nil && src != nil && *src != "" {
						log.Printf("Found video source: %s", *src)
						if te.isValidVideoURL(*src) {
							log.Printf("Detected video via source element")
							return "video"
						}
					}
				}
			}
		}
	}

	// Check page HTML for video patterns in script tags - ENHANCED
	if html, err := page.Context(ctx).HTML(); err == nil {
		// Check for Instagram's video post indicators - Updated patterns
		videoIndicatorPatterns := []string{
			`"__typename":"Video"`,
			`"__typename":"XDTGraphVideo"`,
			`"is_video":true`,
			`"media_type":2`,   // Instagram's video type
			`"media_type":"2"`, // Instagram's video type as string
			`"product_type":"clips"`,
			`"product_type":"igtv"`,
			`"video_url":"`,
			`"video_versions":\s*\[`,
			`"video_dash_manifest":"`,
			`"video_duration":`,
			`"has_audio":`,
			`"original_width":.*"original_height":`, // Video dimensions
			`"playback_duration_secs":`,
		}

		videoIndicatorFound := false
		for _, pattern := range videoIndicatorPatterns {
			if matched, _ := regexp.MatchString(pattern, html); matched {
				log.Printf("Found video indicator pattern: %s", pattern)
				videoIndicatorFound = true
				break
			}
		}

		if videoIndicatorFound {
			log.Printf("Detected video via HTML video indicators - overriding content type detection")
			return "video"
		}

		// Check for Instagram video URLs in the HTML - Updated patterns
		videoURLPatterns := []string{
			`"video_url":\s*"([^"]+)"`,
			`"url":\s*"([^"]+\.mp4[^"]*)"`,
			`video_versions":\s*\[\s*\{\s*"url":\s*"([^"]+)"`,
			`video_versions".*?"url":"([^"]+\.mp4[^"]*)"`,
			`"src":\s*"([^"]+\.mp4[^"]*)"`,
			`"video_dash_manifest":\s*"([^"]+)"`,
			`browser_native_hd_url":\s*"([^"]+)"`,
			`browser_native_sd_url":\s*"([^"]+)"`,
		}

		for _, pattern := range videoURLPatterns {
			re := regexp.MustCompile(pattern)
			if matches := re.FindStringSubmatch(html); len(matches) > 1 {
				url := matches[1]
				log.Printf("Found video URL in HTML via pattern %s: %s", pattern, url)
				if te.isValidVideoURL(url) {
					log.Printf("Detected video via HTML pattern analysis")
					return "video"
				}
			}
		}
	}

	// Check og:image to confirm it's an image post
	if imageMeta, err := page.Context(ctx).Element(`meta[property="og:image"]`); err == nil {
		if content, err := imageMeta.Attribute("content"); err == nil && content != nil && *content != "" {
			log.Printf("Found og:image: %s", *content)
			if strings.Contains(*content, ".jpg") || strings.Contains(*content, ".jpeg") ||
				strings.Contains(*content, ".png") || strings.Contains(*content, ".webp") {
				log.Printf("Detected image via og:image meta tag")
				return "image"
			}
		}
	}

	// Check for actual image elements
	if imgElements, err := page.Context(ctx).Elements("img"); err == nil {
		imageCount := 0
		for _, img := range imgElements {
			if src, err := img.Attribute("src"); err == nil && src != nil && *src != "" {
				if (strings.Contains(*src, "cdninstagram.com") || strings.Contains(*src, "fbcdn.net") || strings.Contains(*src, "scontent")) &&
					(strings.Contains(*src, ".jpg") || strings.Contains(*src, ".jpeg") ||
						strings.Contains(*src, ".png") || strings.Contains(*src, ".webp")) {
					imageCount++
				}
			}
		}
		if imageCount > 0 {
			log.Printf("Detected image via %d content images found", imageCount)
			return "image"
		}
	}

	log.Printf("Could not determine content type, defaulting to image")
	return "image"
}

// extractFromMetaTags - fastest method, extracts from meta tags
func (te *ThreadsExtractor) extractFromMetaTags(page *rod.Page, pageType string) *ExtractResponse {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if pageType == "video" {
		// ONLY extract video URLs for video content
		videoSelectors := []string{
			`meta[property="og:video:url"]`,
			`meta[property="og:video"]`,
			`meta[property="og:video:secure_url"]`,
			`meta[name="twitter:player:stream"]`,
		}

		for _, selector := range videoSelectors {
			if meta, err := page.Context(ctx).Element(selector); err == nil {
				if content, err := meta.Attribute("content"); err == nil && content != nil && *content != "" {
					url := *content
					if te.isValidVideoURL(url) {
						log.Printf("Meta tags found video URL: %s", url)
						return &ExtractResponse{
							MediaURL:  url,
							MediaType: "video",
							Success:   true,
						}
					}
				}
			}
		}
	} else {
		// ONLY extract image URLs for image content - STRICT filtering
		imageSelectors := []string{
			`meta[property="og:image"]`,
			`meta[property="og:image:url"]`,
			`meta[name="twitter:image"]`,
		}

		for _, selector := range imageSelectors {
			if meta, err := page.Context(ctx).Element(selector); err == nil {
				if content, err := meta.Attribute("content"); err == nil && content != nil && *content != "" {
					url := *content
					// CRITICAL: Strictly validate this is an image URL
					if te.isValidImageURL(url) && !te.isValidVideoURL(url) {
						log.Printf("Meta tags found image URL: %s", url)
						return &ExtractResponse{
							MediaURL:  url,
							MediaType: "image",
							Success:   true,
						}
					} else {
						log.Printf("Rejected URL as not a valid image: %s", url)
					}
				}
			}
		}
	}

	return nil
}

// extractFromDOMElements - extracts from video/img elements in DOM (Enhanced for Threads)
func (te *ThreadsExtractor) extractFromDOMElements(page *rod.Page, pageType string) *ExtractResponse {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if pageType == "video" {
		log.Printf("Searching for video elements in DOM...")

		// Optimized video element selectors for Threads (most common first)
		videoSelectors := []string{
			"video[src]",
			"video",
			"[data-testid*='video']",
			"video[autoplay]",
			"[data-video-url]",
		}

		for _, selector := range videoSelectors {
			if videoElements, err := page.Context(ctx).Elements(selector); err == nil && len(videoElements) > 0 {
				log.Printf("Found %d video elements with %s", len(videoElements), selector)

				for _, video := range videoElements {

					// Check src attribute (most common)
					if src, err := video.Attribute("src"); err == nil && src != nil && *src != "" {
						if te.isValidVideoURL(*src) {
							log.Printf("Fast DOM extraction: found video URL")
							return &ExtractResponse{
								MediaURL:  *src,
								MediaType: "video",
								Success:   true,
							}
						}
					}

					// Check data-video-url attribute
					if dataVideoUrl, err := video.Attribute("data-video-url"); err == nil && dataVideoUrl != nil && *dataVideoUrl != "" {
						log.Printf("Found data-video-url: %s", *dataVideoUrl)
						if te.isValidVideoURL(*dataVideoUrl) {
							log.Printf("DOM found valid video URL via data-video-url: %s", *dataVideoUrl)
							return &ExtractResponse{
								MediaURL:  *dataVideoUrl,
								MediaType: "video",
								Success:   true,
							}
						}
					}

					// Check source elements within video
					if sources, err := video.Elements("source"); err == nil {
						log.Printf("Found %d source elements", len(sources))
						for j, source := range sources {
							if src, err := source.Attribute("src"); err == nil && src != nil && *src != "" {
								log.Printf("Found source[%d] src: %s", j, *src)
								if te.isValidVideoURL(*src) {
									log.Printf("DOM found valid video URL via source: %s", *src)
									return &ExtractResponse{
										MediaURL:  *src,
										MediaType: "video",
										Success:   true,
									}
								}
							}
						}
					}

					// Execute JavaScript to get video currentSrc if available
					if currentSrc, err := video.Eval(`() => this.currentSrc || this.src`); err == nil {
						if srcStr := currentSrc.Value.String(); srcStr != "" {
							log.Printf("Found video currentSrc via JavaScript: %s", srcStr)
							if te.isValidVideoURL(srcStr) {
								log.Printf("DOM found valid video URL via JavaScript currentSrc: %s", srcStr)
								return &ExtractResponse{
									MediaURL:  srcStr,
									MediaType: "video",
									Success:   true,
								}
							}
						}
					}
				}
			} else {
				log.Printf("No elements found for selector: %s", selector)
			}
		}

		log.Printf("No video URLs found in DOM elements")
	} else {
		// ONLY look for images when page type is image - strict filtering
		if imgElements, err := page.Context(ctx).Elements("img"); err == nil {
			bestURL := ""
			bestScore := 0

			for _, img := range imgElements {
				if src, err := img.Attribute("src"); err == nil && src != nil && *src != "" {
					url := *src
					// CRITICAL: Double-check this is not a video URL
					if te.isValidImageURL(url) && !te.isValidVideoURL(url) {
						if score := te.scoreImageURL(url); score > bestScore {
							bestURL = url
							bestScore = score
						}
					}
				}
			}

			if bestURL != "" && bestScore > 50 {
				log.Printf("DOM found image URL: %s (score: %d)", bestURL, bestScore)
				return &ExtractResponse{
					MediaURL:  bestURL,
					MediaType: "image",
					Success:   true,
				}
			}
		}
	}

	return nil
}

// extractFromSourceCode - analyzes page source for embedded media URLs
func (te *ThreadsExtractor) extractFromSourceCode(page *rod.Page, pageType string) *ExtractResponse {
	// Remove unused context for faster execution

	// Get page HTML content with quick timeout
	quickCtx, quickCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer quickCancel()

	html, err := page.Context(quickCtx).HTML()
	if err != nil {
		log.Printf("Failed to get HTML content quickly: %v", err)
		return nil
	}

	// ALWAYS check for video patterns first, regardless of detected pageType
	// Threads-specific video patterns (priority order)
	videoPatterns := []string{
		// Threads primary video patterns
		`video_versions":\s*\[\s*\{\s*"url":\s*"([^"]+)"`,  // Primary video versions array
		`"video_url":\s*"([^"]+)"`,                         // Direct video URL
		`"url":\s*"([^"]+\.mp4[^"]*)"`,                     // Direct MP4 URL in JSON
		`video_versions"[^}]*"url":\s*"([^"]+\.mp4[^"]*)"`, // Video versions with MP4
		`"playback_url":\s*"([^"]+)"`,                      // Playback URL

		// Threads/Meta CDN patterns
		`https://[^"'\s]*video[^"'\s]*fbcdn\.net[^"'\s]*\.mp4[^"'\s]*`, // Facebook video CDN
		`https://[^"'\s]*scontent[^"'\s]*\.mp4[^"'\s]*`,                // Scontent CDN MP4s
		`https://[^"'\s]*cdninstagram\.com[^"'\s]*\.mp4[^"'\s]*`,       // Instagram CDN (used by Threads)

		// Generic video patterns (fallback)
		`"src":\s*"([^"]+\.mp4[^"]*)"`,        // Source attribute
		`browser_native_hd_url":\s*"([^"]+)"`, // Browser native HD
		`browser_native_sd_url":\s*"([^"]+)"`, // Browser native SD
		`"video_dash_manifest":\s*"([^"]+)"`,  // DASH manifest

		// Additional Instagram patterns
		`candidates":\s*\[[^}]*"url":\s*"([^"]+\.mp4[^"]*)"`, // Candidates array
		`video_resources"[^}]*"src":\s*"([^"]+\.mp4[^"]*)"`,  // Video resources
		`data-video-url="([^"]+)"`,                           // Data attribute
		`data-src="([^"]+\.mp4[^"]*)"`,                       // Data src attribute
	}

	// Quick pattern search
	log.Printf("Searching HTML patterns...")

	for _, pattern := range videoPatterns {
		re := regexp.MustCompile(pattern)
		matches := re.FindAllStringSubmatch(html, -1)

		if len(matches) > 0 {
			for _, match := range matches {
				if len(match) > 1 {
					url := match[1]
					// Unescape URL if needed
					url = strings.ReplaceAll(url, "\\u0026", "&")
					url = strings.ReplaceAll(url, "\\/", "/")

					if te.isValidVideoURL(url) {
						// Extract additional metadata
						videoID, title, duration, videoUrls, metadata := te.extractVideoMetadata(html)
						return &ExtractResponse{
							MediaURL:  url,
							MediaType: "video",
							Success:   true,
							VideoID:   videoID,
							Title:     title,
							Duration:  duration,
							VideoUrls: videoUrls,
							Metadata:  metadata,
						}
					}
				}
			}
		}
	}

	// No video found, try image patterns if needed

	// If no video found, try image patterns (only if pageType suggests image content)
	if pageType == "image" {
		// Look for image URLs only when pageType is image - Instagram priority order
		imagePatterns := []string{
			`"display_url":\s*"([^"]+)"`,                                             // Instagram primary display URL
			`"image_url":\s*"([^"]+)"`,                                               // General image URL
			`"url":\s*"([^"]+\.(jpg|jpeg|png|webp)[^"]*)"`,                           // URL with image extension
			`https://[^"'\s]*cdninstagram\.com[^"'\s]*\.(jpg|jpeg|png|webp)[^"'\s]*`, // Instagram CDN
			`https://[^"'\s]*fbcdn\.net[^"'\s]*\.(jpg|jpeg|png|webp)[^"'\s]*`,        // Facebook CDN
			`https://[^"'\s]*scontent[^"'\s]*\.(jpg|jpeg|png|webp)[^"'\s]*`,          // Scontent CDN
		}

		for _, pattern := range imagePatterns {
			re := regexp.MustCompile(pattern)
			if matches := re.FindStringSubmatch(html); len(matches) > 1 {
				url := matches[1]
				// Unescape URL if needed
				url = strings.ReplaceAll(url, "\\u0026", "&")
				url = strings.ReplaceAll(url, "\\/", "/")

				log.Printf("Found potential image URL: %s", url)
				if te.isValidImageURL(url) && !te.isValidVideoURL(url) {
					log.Printf("Valid image URL found: %s", url)
					return &ExtractResponse{
						MediaURL:  url,
						MediaType: "image",
						Success:   true,
					}
				}
			}
		}
	}

	log.Printf("No valid URLs found in source code for pageType: %s", pageType)
	return nil
}

// extractVideoMetadata extracts video metadata and multiple URLs from HTML content
func (te *ThreadsExtractor) extractVideoMetadata(html string) (string, string, int64, map[string]string, map[string]string) {
	var videoID, title string
	var duration int64
	videoUrls := make(map[string]string)
	metadata := make(map[string]string)

	// Facebook metadata patterns based on user's request
	metadataPatterns := map[string]*regexp.Regexp{
		"video_id":     regexp.MustCompile(`"video_id":"?(\d+)"?`),
		"title":        regexp.MustCompile(`<title>([^<]+)</title>`),
		"duration":     regexp.MustCompile(`"playable_duration_in_ms":(\d+)`),
		"browser_hd":   regexp.MustCompile(`"browser_native_hd_url":"([^"]+)"`),
		"browser_sd":   regexp.MustCompile(`"browser_native_sd_url":"([^"]+)"`),
		"hd_src":       regexp.MustCompile(`"hd_src":"([^"]+)"`),
		"sd_src":       regexp.MustCompile(`"sd_src":"([^"]+)"`),
		"playable_url": regexp.MustCompile(`"playable_url":"([^"]+)"`),
	}

	// Extract video ID
	if matches := metadataPatterns["video_id"].FindStringSubmatch(html); len(matches) > 1 {
		videoID = matches[1]
		metadata["video_id"] = videoID
		log.Printf("Extracted video ID: %s", videoID)
	}

	// Extract title
	if matches := metadataPatterns["title"].FindStringSubmatch(html); len(matches) > 1 {
		title = strings.TrimSpace(matches[1])
		// Clean up common Facebook title patterns
		title = strings.ReplaceAll(title, " | Facebook", "")
		title = strings.ReplaceAll(title, " - Facebook", "")
		metadata["title"] = title
		log.Printf("Extracted title: %s", title)
	}

	// Extract duration (convert from milliseconds to seconds)
	if matches := metadataPatterns["duration"].FindStringSubmatch(html); len(matches) > 1 {
		if durationMs, err := strconv.ParseInt(matches[1], 10, 64); err == nil {
			duration = durationMs / 1000 // Convert to seconds
			metadata["duration"] = fmt.Sprintf("%d seconds", duration)
			log.Printf("Extracted duration: %d seconds", duration)
		}
	}

	// Extract browser HD URL
	if matches := metadataPatterns["browser_hd"].FindStringSubmatch(html); len(matches) > 1 {
		url := strings.ReplaceAll(matches[1], "\\u0026", "&")
		url = strings.ReplaceAll(url, "\\/", "/")
		videoUrls["browser_hd"] = url
		log.Printf("Extracted browser HD URL: %s", url)
	}

	// Extract browser SD URL
	if matches := metadataPatterns["browser_sd"].FindStringSubmatch(html); len(matches) > 1 {
		url := strings.ReplaceAll(matches[1], "\\u0026", "&")
		url = strings.ReplaceAll(url, "\\/", "/")
		videoUrls["browser_sd"] = url
		log.Printf("Extracted browser SD URL: %s", url)
	}

	// Extract HD source
	if matches := metadataPatterns["hd_src"].FindStringSubmatch(html); len(matches) > 1 {
		url := strings.ReplaceAll(matches[1], "\\u0026", "&")
		url = strings.ReplaceAll(url, "\\/", "/")
		videoUrls["hd_src"] = url
		log.Printf("Extracted HD source: %s", url)
	}

	// Extract SD source
	if matches := metadataPatterns["sd_src"].FindStringSubmatch(html); len(matches) > 1 {
		url := strings.ReplaceAll(matches[1], "\\u0026", "&")
		url = strings.ReplaceAll(url, "\\/", "/")
		videoUrls["sd_src"] = url
		log.Printf("Extracted SD source: %s", url)
	}

	// Extract playable URL
	if matches := metadataPatterns["playable_url"].FindStringSubmatch(html); len(matches) > 1 {
		url := strings.ReplaceAll(matches[1], "\\u0026", "&")
		url = strings.ReplaceAll(url, "\\/", "/")
		videoUrls["playable_url"] = url
		log.Printf("Extracted playable URL: %s", url)
	}

	return videoID, title, duration, videoUrls, metadata
}

// isValidVideoURL validates if URL is likely a video
func (te *ThreadsExtractor) isValidVideoURL(url string) bool {
	if url == "" {
		return false
	}

	// Exclude obvious non-video URLs
	if strings.Contains(url, ".jpg") || strings.Contains(url, ".jpeg") ||
		strings.Contains(url, ".png") || strings.Contains(url, ".webp") ||
		strings.Contains(url, ".gif") {
		return false
	}

	// Check for video file extensions
	if strings.Contains(url, ".mp4") || strings.Contains(url, ".webm") ||
		strings.Contains(url, ".mov") || strings.Contains(url, ".m4v") ||
		strings.Contains(url, ".avi") || strings.Contains(url, ".mkv") {
		log.Printf("Valid video URL by extension: %s", url)
		return true
	}

	// Check for Facebook video CDN patterns
	facebookVideoCDNs := []string{
		"video.fbcdn.net",
		"scontent-video",
		"video.xx.fbcdn.net",
		"video-",
	}

	for _, cdn := range facebookVideoCDNs {
		if strings.Contains(url, cdn) {
			log.Printf("Valid video URL by Facebook CDN pattern: %s", url)
			return true
		}
	}

	// Check for Threads video patterns (highest priority)
	if strings.Contains(url, "cdninstagram.com") {
		log.Printf("Valid video URL by Threads CDN: %s", url)
		return true
	}

	// Check for Facebook CDN video patterns (Threads uses Facebook CDN)
	if strings.Contains(url, "fbcdn.net") || strings.Contains(url, "scontent") {
		// More specific check for video indicators
		if strings.Contains(url, "video") || strings.Contains(url, ".mp4") {
			log.Printf("Valid video URL by Facebook CDN pattern: %s", url)
			return true
		}
	}

	// Check if URL contains video-related keywords and is from a trusted domain
	if strings.Contains(url, "threads.net") || strings.Contains(url, "fbcdn.net") ||
		strings.Contains(url, "scontent") {
		videoKeywords := []string{"video", "playable", "stream", "media", ".mp4", ".webm", ".mov"}
		for _, keyword := range videoKeywords {
			if strings.Contains(url, keyword) {
				log.Printf("Valid video URL by keyword '%s': %s", keyword, url)
				return true
			}
		}
	}

	log.Printf("URL rejected as not a valid video: %s", url)
	return false
}

// isValidImageURL validates if URL is likely a content image (not UI element)
func (te *ThreadsExtractor) isValidImageURL(url string) bool {
	if url == "" {
		return false
	}

	// CRITICAL: Exclude video URLs that might be misclassified as images
	if strings.Contains(url, ".mp4") || strings.Contains(url, ".webm") ||
		strings.Contains(url, ".mov") || strings.Contains(url, "video") {
		return false
	}

	// Must be from Threads or Facebook CDN (Threads uses Facebook CDN)
	if !strings.Contains(url, "cdninstagram.com") && !strings.Contains(url, "fbcdn.net") && !strings.Contains(url, "scontent") {
		return false
	}

	// Must have image file extension or be clearly an image
	hasImageExtension := strings.Contains(url, ".jpg") || strings.Contains(url, ".jpeg") ||
		strings.Contains(url, ".png") || strings.Contains(url, ".webp")

	if !hasImageExtension {
		return false
	}

	// Exclude profile pictures, logos, UI elements
	excludePatterns := []string{"profile", "avatar", "logo", "icon", "badge", "button", "default", "safe_image"}
	for _, pattern := range excludePatterns {
		if strings.Contains(url, pattern) {
			return false
		}
	}

	return true
}

// scoreImageURL gives a quality score to image URLs (higher = better)
func (te *ThreadsExtractor) scoreImageURL(url string) int {
	if !te.isValidImageURL(url) {
		return 0
	}

	score := 50 // base score for valid images

	// Higher scores for larger images
	if strings.Contains(url, "1080x1080") {
		score += 50
	} else if strings.Contains(url, "720x720") {
		score += 30
	} else if strings.Contains(url, "640x640") {
		score += 20
	}

	// Bonus for high-quality indicators
	if strings.Contains(url, "full_res") || strings.Contains(url, "original") {
		score += 25
	}

	return score
}

// fallbackExtraction - simpler extraction when all strategies fail
func (te *ThreadsExtractor) fallbackExtraction(page *rod.Page, pageType string) *ExtractResponse {
	log.Printf("Starting fallback extraction for %s content", pageType)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if pageType == "image" {
		// Simple image extraction - look for any Instagram CDN images
		if imgElements, err := page.Context(ctx).Elements("img"); err == nil {
			for _, img := range imgElements {
				if src, err := img.Attribute("src"); err == nil && src != nil && *src != "" {
					url := *src
					log.Printf("Checking image URL: %s", url)

					// More lenient validation for fallback
					if strings.Contains(url, "cdninstagram.com") || strings.Contains(url, "fbcdn.net") {
						// Exclude obvious UI elements but be more permissive
						if !strings.Contains(url, "profile") &&
							!strings.Contains(url, "avatar") &&
							!strings.Contains(url, "logo") &&
							!strings.Contains(url, ".mp4") {
							log.Printf("Fallback found image: %s", url)
							return &ExtractResponse{
								MediaURL:  url,
								MediaType: "image",
								Success:   true,
							}
						}
					}
				}
			}
		}
	} else {
		// Simple video extraction
		if videoElements, err := page.Context(ctx).Elements("video"); err == nil {
			for _, video := range videoElements {
				if src, err := video.Attribute("src"); err == nil && src != nil && *src != "" {
					url := *src
					log.Printf("Fallback found video: %s", url)
					return &ExtractResponse{
						MediaURL:  url,
						MediaType: "video",
						Success:   true,
					}
				}
			}
		}
	}

	log.Printf("Fallback extraction failed")
	return nil
}

// handleExtract handles the API endpoint for extracting media URLs
func handleExtract(te *ThreadsExtractor) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Set CORS headers
		w.Header().Set("Access-Control-Allow-Origin", "https://threadsvid.com")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		w.Header().Set("Content-Type", "application/json")

		// Handle preflight request
		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}

		if r.Method != "POST" {
			w.WriteHeader(http.StatusMethodNotAllowed)
			json.NewEncoder(w).Encode(ErrorResponse{
				Error:   "Method not allowed",
				Success: false,
			})
			return
		}

		var req ExtractRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(ErrorResponse{
				Error:   "Invalid JSON payload",
				Success: false,
			})
			return
		}

		if req.URL == "" {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(ErrorResponse{
				Error:   "URL is required",
				Success: false,
			})
			return
		}

		// Extract media URL
		result, err := te.extractMediaURL(req.URL)
		if err != nil {
			log.Printf("Extraction error for URL %s: %v", req.URL, err)
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(ErrorResponse{
				Error:   err.Error(),
				Success: false,
			})
			return
		}

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(result)
	}
}

// handleDownload handles media download requests with CORS support
func handleDownload() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Set CORS headers
		w.Header().Set("Access-Control-Allow-Origin", "https://threadsvid.com")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

		// Handle preflight request
		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}

		if r.Method != "GET" {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		// Get URL and filename from query parameters
		mediaURL := r.URL.Query().Get("url")
		filename := r.URL.Query().Get("filename")

		if mediaURL == "" {
			http.Error(w, "URL parameter is required", http.StatusBadRequest)
			return
		}

		log.Printf("Proxying download request for: %s", mediaURL)

		// Create HTTP client with timeout
		client := &http.Client{
			Timeout: 30 * time.Second,
		}

		// Fetch the media file
		resp, err := client.Get(mediaURL)
		if err != nil {
			log.Printf("Failed to fetch media: %v", err)
			http.Error(w, "Failed to fetch media", http.StatusInternalServerError)
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			log.Printf("Media fetch failed with status: %d", resp.StatusCode)
			http.Error(w, "Media not found", http.StatusNotFound)
			return
		}

		// Set headers for download
		if filename != "" {
			w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", filename))
		}
		w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
		w.Header().Set("Content-Length", resp.Header.Get("Content-Length"))

		// Stream the content
		_, err = io.Copy(w, resp.Body)
		if err != nil {
			log.Printf("Failed to stream media: %v", err)
			return
		}

		log.Printf("Successfully proxied download for: %s", filename)
	}
}

// serveStaticFiles serves the frontend files
func serveStaticFiles() {
	fs := http.FileServer(http.Dir("."))
	http.Handle("/", fs)
}

func main() {
	// Initialize the Threads extractor
	extractor, err := NewThreadsExtractor()
	if err != nil {
		log.Fatalf("Failed to initialize Threads extractor: %v", err)
	}
	defer extractor.Close()

	// Setup routes
	serveStaticFiles()
	http.HandleFunc("/api/extract", handleExtract(extractor))
	http.HandleFunc("/api/download", handleDownload())

	// Health check endpoint
	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		// Set CORS headers
		w.Header().Set("Access-Control-Allow-Origin", "https://threadsvid.com")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		w.Header().Set("Content-Type", "application/json")

		json.NewEncoder(w).Encode(map[string]string{
			"status": "healthy",
			"time":   time.Now().Format(time.RFC3339),
		})
	})

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	log.Printf("Threads Video Downloader server starting on port %s", port)
	log.Printf("Frontend available at: http://0.0.0.0:%s", port)
	log.Printf("API endpoint: http://0.0.0.0:%s/api/extract", port)

	if err := http.ListenAndServe("0.0.0.0:"+port, nil); err != nil {
		log.Fatalf("Server failed to start: %v", err)
	}
}
