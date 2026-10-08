// Package buildinfo holds version metadata injected via -ldflags at build time.
package buildinfo

// Set with -X github.com/yyewolf/ssarchiver/internal/buildinfo.Version=... etc.
var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)

// UserAgent is sent on every ScoreSaber request.
func UserAgent() string {
	return "SSArchiver/" + Version + " (+https://github.com/yyewolf/SSArchiver)"
}
