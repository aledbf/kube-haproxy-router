/*
Copyright 2014 The Kubernetes Authors All rights reserved.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package proxy

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"net/url"
	"path"
	"strings"

	"github.com/golang/glog"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"

	"github.com/GoogleCloudPlatform/kubernetes/pkg/util"
)

// atomsToAttrs states which attributes of which tags require URL substitution.
// Sources: http://www.w3.org/TR/REC-html40/index/attributes.html
//          http://www.w3.org/html/wg/drafts/html/master/index.html#attributes-1
var atomsToAttrs = map[atom.Atom]util.StringSet{
	atom.A:          util.NewStringSet("href"),
	atom.Applet:     util.NewStringSet("codebase"),
	atom.Area:       util.NewStringSet("href"),
	atom.Audio:      util.NewStringSet("src"),
	atom.Base:       util.NewStringSet("href"),
	atom.Blockquote: util.NewStringSet("cite"),
	atom.Body:       util.NewStringSet("background"),
	atom.Button:     util.NewStringSet("formaction"),
	atom.Command:    util.NewStringSet("icon"),
	atom.Del:        util.NewStringSet("cite"),
	atom.Embed:      util.NewStringSet("src"),
	atom.Form:       util.NewStringSet("action"),
	atom.Frame:      util.NewStringSet("longdesc", "src"),
	atom.Head:       util.NewStringSet("profile"),
	atom.Html:       util.NewStringSet("manifest"),
	atom.Iframe:     util.NewStringSet("longdesc", "src"),
	atom.Img:        util.NewStringSet("longdesc", "src", "usemap"),
	atom.Input:      util.NewStringSet("src", "usemap", "formaction"),
	atom.Ins:        util.NewStringSet("cite"),
	atom.Link:       util.NewStringSet("href"),
	atom.Object:     util.NewStringSet("classid", "codebase", "data", "usemap"),
	atom.Q:          util.NewStringSet("cite"),
	atom.Script:     util.NewStringSet("src"),
	atom.Source:     util.NewStringSet("src"),
	atom.Video:      util.NewStringSet("poster", "src"),

	// TODO: css URLs hidden in style elements.
}

// Transport is a transport for text/html content that replaces URLs in html
// content with the prefix of the proxy server
type Transport struct {
	Scheme      string
	Host        string
	PathPrepend string

	http.RoundTripper
}

// RoundTrip implements the http.RoundTripper interface
func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Add reverse proxy headers.
	forwardedURI := path.Join(t.PathPrepend, req.URL.Path)
	if strings.HasSuffix(req.URL.Path, "/") {
		forwardedURI = forwardedURI + "/"
	}
	req.Header.Set("X-Forwarded-Uri", forwardedURI)
	req.Header.Set("X-Forwarded-Host", t.Host)
	req.Header.Set("X-Forwarded-Proto", t.Scheme)

	rt := t.RoundTripper
	if rt == nil {
		rt = http.DefaultTransport
	}
	resp, err := rt.RoundTrip(req)

	if err != nil {
		message := fmt.Sprintf("Error: '%s'\nTrying to reach: '%v'", err.Error(), req.URL.String())
		resp = &http.Response{
			StatusCode: http.StatusServiceUnavailable,
			Body:       ioutil.NopCloser(strings.NewReader(message)),
		}
		return resp, nil
	}

	if redirect := resp.Header.Get("Location"); redirect != "" {
		resp.Header.Set("Location", t.rewriteURL(redirect, req.URL))
		return resp, nil
	}

	cType := resp.Header.Get("Content-Type")
	cType = strings.TrimSpace(strings.SplitN(cType, ";", 2)[0])
	if cType != "text/html" {
		// Do nothing, simply pass through
		return resp, nil
	}

	return t.rewriteResponse(req, resp)
}

// rewriteURL rewrites a single URL to go through the proxy, if the URL refers
// to the same host as sourceURL, which is the page on which the target URL
// occurred. If any error occurs (e.g. parsing), it returns targetURL.
func (t *Transport) rewriteURL(targetURL string, sourceURL *url.URL) string {
	url, err := url.Parse(targetURL)
	if err != nil {
		return targetURL
	}

	isDifferentHost := url.Host != "" && url.Host != sourceURL.Host
	isRelative := !strings.HasPrefix(url.Path, "/")
	if isDifferentHost || isRelative {
		return targetURL
	}

	url.Scheme = t.Scheme
	url.Host = t.Host
	origPath := url.Path
	url.Path = path.Join(t.PathPrepend, url.Path)

	if strings.HasSuffix(origPath, "/") {
		// Add back the trailing slash, which was stripped by path.Join().
		url.Path += "/"
	}

	return url.String()
}

// rewriteHTML scans the HTML for tags with url-valued attributes, and updates
// those values with the urlRewriter function. The updated HTML is output to the
// writer.
func rewriteHTML(reader io.Reader, writer io.Writer, urlRewriter func(string) string) error {
	// Note: This assumes the content is UTF-8.
	tokenizer := html.NewTokenizer(reader)

	var err error
	for err == nil {
		tokenType := tokenizer.Next()
		switch tokenType {
		case html.ErrorToken:
			err = tokenizer.Err()
		case html.StartTagToken, html.SelfClosingTagToken:
			token := tokenizer.Token()
			if urlAttrs, ok := atomsToAttrs[token.DataAtom]; ok {
				for i, attr := range token.Attr {
					if urlAttrs.Has(attr.Key) {
						token.Attr[i].Val = urlRewriter(attr.Val)
					}
				}
			}
			_, err = writer.Write([]byte(token.String()))
		default:
			_, err = writer.Write(tokenizer.Raw())
		}
	}
	if err != io.EOF {
		return err
	}
	return nil
}

// rewriteResponse modifies an HTML response by updating absolute links refering
// to the original host to instead refer to the proxy transport.
func (t *Transport) rewriteResponse(req *http.Request, resp *http.Response) (*http.Response, error) {
	origBody := resp.Body
	defer origBody.Close()

	newContent := &bytes.Buffer{}
	var reader io.Reader = origBody
	var writer io.Writer = newContent
	encoding := resp.Header.Get("Content-Encoding")
	switch encoding {
	case "gzip":
		var err error
		reader, err = gzip.NewReader(reader)
		if err != nil {
			return nil, fmt.Errorf("errorf making gzip reader: %v", err)
		}
		gzw := gzip.NewWriter(writer)
		defer gzw.Close()
		writer = gzw
	// TODO: support flate, other encodings.
	case "":
		// This is fine
	default:
		// Some encoding we don't understand-- don't try to parse this
		glog.Errorf("Proxy encountered encoding %v for text/html; can't understand this so not fixing links.", encoding)
		return resp, nil
	}

	urlRewriter := func(targetUrl string) string {
		return t.rewriteURL(targetUrl, req.URL)
	}
	err := rewriteHTML(reader, writer, urlRewriter)
	if err != nil {
		glog.Errorf("Failed to rewrite URLs: %v", err)
		return resp, err
	}

	resp.Body = ioutil.NopCloser(newContent)
	// Update header node with new content-length
	// TODO: Remove any hash/signature headers here?
	resp.Header.Del("Content-Length")
	resp.ContentLength = int64(newContent.Len())

	return resp, err
}
