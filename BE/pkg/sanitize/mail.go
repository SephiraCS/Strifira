package sanitize

import (
	"bytes"
	"net/url"
	"strings"

	"golang.org/x/net/html"
)

// PrepareMailHTML keeps image references inert until the reader requests them.
// CID references are replaced with IDs allocated for this message's attachments,
// so the association survives storage without changing the shared HTML policy.
func PrepareMailHTML(input string, cidImages map[string]string) string {
	doc, err := html.Parse(strings.NewReader(input))
	if err != nil {
		return ""
	}
	visitMailImages(doc, func(n *html.Node) {
		source := ""
		attrs := n.Attr[:0]
		for _, a := range n.Attr {
			if a.Key == "src" {
				source = a.Val
			}
			if a.Key != "src" && a.Key != "srcset" && a.Key != "data-mail-image" {
				attrs = append(attrs, a)
			}
		}
		source = strings.TrimSpace(source)
		descriptor := "invalid:"
		if strings.HasPrefix(strings.ToLower(source), "cid:") {
			cid, err := url.PathUnescape(source[4:])
			if err == nil {
				if id := cidImages[strings.Trim(cid, "<> ")]; id != "" {
					descriptor = "attachment:" + id
				}
			}
		} else if IsMailRemoteImage(source) {
			descriptor = source
		} else if source != "" {
			descriptor = "blocked:"
		}
		n.Attr = append(attrs, html.Attribute{Key: "data-mail-image", Val: descriptor})
	})
	var out bytes.Buffer
	if html.Render(&out, doc) != nil {
		return ""
	}
	return mailPolicy.Sanitize(out.String())
}

// IsMailRemoteImage allows only credential-free HTTPS on the standard port.
// Network destinations and every redirect are checked separately when fetched.
func IsMailRemoteImage(source string) bool {
	u, err := url.Parse(source)
	return err == nil && u.Scheme == "https" && u.Hostname() != "" && u.User == nil &&
		(u.Port() == "" || u.Port() == "443") && u.Opaque == "" && u.Fragment == ""
}

// MailImageSources also recognizes HTTPS references in messages cached before
// the inert image representation was introduced. It never fetches a resource.
func MailImageSources(input string) []string {
	doc, err := html.Parse(strings.NewReader(input))
	if err != nil {
		return nil
	}
	var sources []string
	visitMailImages(doc, func(n *html.Node) {
		for _, a := range n.Attr {
			if a.Key == "data-mail-image" || (a.Key == "src" && IsMailRemoteImage(a.Val)) {
				sources = append(sources, a.Val)
			}
		}
	})
	return sources
}

func visitMailImages(n *html.Node, visit func(*html.Node)) {
	if n.Type == html.ElementNode && n.Data == "img" {
		visit(n)
	}
	for child := n.FirstChild; child != nil; child = child.NextSibling {
		visitMailImages(child, visit)
	}
}

// NeedsMailImageRepair identifies CID references erased by the old sanitizer.
func NeedsMailImageRepair(input string) bool {
	doc, err := html.Parse(strings.NewReader(input))
	if err != nil {
		return false
	}
	needs := false
	visitMailImages(doc, func(n *html.Node) {
		for _, a := range n.Attr {
			if a.Key == "src" || a.Key == "data-mail-image" {
				return
			}
		}
		needs = true
	})
	return needs
}
