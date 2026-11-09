package catalog

import "slices"

// Releases returns the releases of the Plugin name that target apiVersion,
// the Plugin API version this Laneway speaks, newest first. It is empty if
// the Catalog doesn't list the Plugin.
func (c *Catalog) Releases(name string, apiVersion int) []Release {
	var releases []Release
	for _, p := range c.Plugins {
		if p.Name != name {
			continue
		}
		for _, r := range p.Releases {
			if r.APIVersion == apiVersion {
				releases = append(releases, r)
			}
		}
	}
	slices.SortStableFunc(releases, func(a, b Release) int { return compareVersions(b.Version, a.Version) })
	return releases
}

// CompareVersions compares two release versions, such as "1.2.0" and
// "1.10.0", as the Catalog orders them: -1 if a is older, 1 if newer, 0 if
// the same. A version that doesn't parse sorts as 0.
func CompareVersions(a, b string) int {
	return compareVersions(a, b)
}
