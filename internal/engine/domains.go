package engine

import (
	"fmt"
	"regexp"
)

// ServiceDomain is a Domain under the Dashboard's, <Label>.<Dashboard
// Domain>, at which Laneway serves one of an Engine's published container
// ports over HTTPS, such as Mailpit's inbox at mail.laneway.localhost. It
// serves the Service of the Engine's default version if one is served, else
// the first served one, starting it on the first request.
type ServiceDomain struct {
	// Label is the Domain's first label, such as "mail". It is unique
	// across the installed Engines.
	Label string `toml:"label" json:"label"`
	// Port is the container port requests are forwarded to: Port or
	// WebPort.
	Port int `toml:"port" json:"port"`
}

// domainLabel is what a Service Domain's label may be: one DNS label,
// lowercase.
var domainLabel = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

// maxLabel is the longest a DNS label may be.
const maxLabel = 63

// checkDomains reports the first Service Domain Laneway can't serve.
func (m *Manifest) checkDomains() error {
	for i, d := range m.Domains {
		key := fmt.Sprintf("domains[%d]", i)
		switch {
		case d.Label == "":
			return fmt.Errorf("%s.label is missing: give the Domain's label under the Dashboard's Domain, such as \"mail\"", key)
		case len(d.Label) > maxLabel:
			return fmt.Errorf("%s.label must be at most %d characters, as a Domain label is, not %d", key, maxLabel, len(d.Label))
		case !domainLabel.MatchString(d.Label):
			return fmt.Errorf("%s.label must be one Domain label: lowercase letters, digits and -, not starting or ending with -, not %q", key, d.Label)
		case d.Port == 0:
			return fmt.Errorf("%s.port is missing: give the container port the Domain forwards to, port or web_port", key)
		case d.Port != m.Port && d.Port != m.WebPort:
			return fmt.Errorf("%s.port is %d, which isn't published: a Service Domain can only forward to port or web_port", key, d.Port)
		}
		for _, other := range m.Domains[:i] {
			if other.Label == d.Label {
				return fmt.Errorf("%s.label is %q, which the Engine already declares", key, d.Label)
			}
		}
	}
	return nil
}

// Domain is the Engine's Service Domain whose label is label.
func (m *Manifest) Domain(label string) (ServiceDomain, bool) {
	for _, d := range m.Domains {
		if d.Label == label {
			return d, true
		}
	}
	return ServiceDomain{}, false
}

// URL is the Service Domain's URL, under dashboardDomain, with no trailing
// slash, such as https://s3.laneway.localhost.
func (d ServiceDomain) URL(dashboardDomain string) string {
	return "https://" + d.Label + "." + dashboardDomain
}

// endpointVariable is the Service variable an Engine with Service Domains
// gives: {<engine>_endpoint}, its first Service Domain's URL.
func (m *Manifest) endpointVariable() string {
	return m.Name + "_endpoint"
}

// DomainValues are the values of the Service variables the Engine's Service
// Domains give, under dashboardDomain: {<engine>_endpoint}, if it has any.
func (m *Manifest) DomainValues(dashboardDomain string) map[string]string {
	if len(m.Domains) == 0 {
		return nil
	}
	return map[string]string{m.endpointVariable(): m.Endpoint(dashboardDomain)}
}

// Endpoint is the URL of the Engine's first Service Domain under
// dashboardDomain, the value of {<engine>_endpoint}; "" if it has none.
func (m *Manifest) Endpoint(dashboardDomain string) string {
	if len(m.Domains) == 0 {
		return ""
	}
	return m.Domains[0].URL(dashboardDomain)
}
