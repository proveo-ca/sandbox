package egresspolicy

// DefaultSinks are domain suffixes that exist to RECEIVE exfil; denied for all
// methods (so even a GET to webhook.site is blocked). Curated starter set,
// refreshable later. Operators extend/override via PROVEO_EGRESS_* config.
//
// SPEC: _spec/internal/egresspolicy/egress-policy-layers.puml
var DefaultSinks = []string{
	// paste bins
	"pastebin.com", "hastebin.com", "paste.ee", "ix.io", "0x0.st", "dpaste.com", "ghostbin.com",
	// request/webhook capture
	"webhook.site", "requestbin.com", "pipedream.net", "requestcatcher.com", "beeceptor.com",
	// tunnels
	"ngrok.io", "ngrok-free.app", "trycloudflare.com", "serveo.net", "localtunnel.me", "loca.lt",
	// chat webhooks
	"discord.com", "discordapp.com", "hooks.slack.com", "api.telegram.org",
	// url shorteners
	"bit.ly", "tinyurl.com", "t.co", "is.gd",
}

// DefaultWriteHosts are non-provider hosts where write methods stay allowed by
// default so real agent workflows (git push, opening PRs) keep working. Extend
// via PROVEO_EGRESS_WRITE_HOSTS.
var DefaultWriteHosts = []string{
	"github.com", "api.github.com", "gitlab.com", "bitbucket.org",
}
