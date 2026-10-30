// Package readme renders the README of a package version to sanitized HTML on the
// server (R-UI-008, R-API-029), so that one renderer and one sanitizer policy serve
// every client.
//
// The pipeline has three steps. goldmark turns CommonMark plus the GitHub-style
// tables, strikethrough, autolinks and task lists into HTML and lets raw HTML
// through; bluemonday keeps only the allowlist of R-UI-008; a pass over the parsed
// result applies what an allowlist cannot say: which links and images may stay, the
// rel of a link, disabled checkboxes, and the anchor of every heading. The UI binds
// the result with [innerHTML], where Angular's sanitizer and the Trusted Types
// policy are further layers; the component repeats the heading rule so that the
// links of a table of contents resolve (Q-UI-71), and [HeadingSlug] is that rule.
package readme
