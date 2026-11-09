package sdk

// EffectHTTP is the kind of an http effect: a request Laneway sends for the
// Plugin, since its module has no sockets. Laneway sends it only to hosts
// the manifest lists under capabilities.http.
const EffectHTTP = "http"

// HTTPRequest is an http effect's request.
type HTTPRequest struct {
	// Method is the request's method: GET, HEAD, POST, PUT, PATCH or
	// DELETE.
	Method string `json:"method"`
	// URL is where it goes, an http or https URL whose host the manifest
	// lists.
	URL string `json:"url"`
	// Headers are its headers, such as {"Title": "Worktree Ready"}.
	Headers map[string]string `json:"headers,omitempty"`
	// Body is its body.
	Body string `json:"body,omitempty"`
}

// HTTP is an effect that sends r. It needs the "http" effect, and r's host
// listed under capabilities.http. A response other than 2xx fails the
// effect.
func HTTP(r HTTPRequest) Effect { return Effect{Kind: EffectHTTP, HTTP: &r} }
