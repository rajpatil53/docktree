package identity

import "strings"

// DNSLabel converts a docktree identity component (slug, app, or service name)
// into a DNS-safe label: lowercase, with every character outside [a-z0-9-]
// collapsed to '-', and leading/trailing '-' trimmed. Docktree slugs are
// validated ^[a-z0-9_]+$, so in practice this maps '_' to '-'. It is the single
// source of truth shared by the proxy host label (compose render) and the CLI
// URL output (open/explain).
func DNSLabel(component string) string {
	var b strings.Builder
	for _, r := range component {
		switch {
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r + ('a' - 'A'))
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-")
}
