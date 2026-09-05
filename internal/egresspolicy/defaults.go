package egresspolicy

// SPEC: _spec/internal/egresspolicy/egress-policy-layers.puml
var DefaultSinks = []string{
	"pastebin.com", "hastebin.com", "paste.ee", "ix.io", "0x0.st", "dpaste.com", "ghostbin.com",
	"webhook.site", "requestbin.com", "pipedream.net", "requestcatcher.com", "beeceptor.com",
	"ngrok.io", "ngrok-free.app", "trycloudflare.com", "serveo.net", "localtunnel.me", "loca.lt",
	"discord.com", "discordapp.com", "hooks.slack.com", "api.telegram.org",
	"bit.ly", "tinyurl.com", "t.co", "is.gd",
}

var DefaultWriteHosts = []string{
	"github.com", "api.github.com", "gitlab.com", "bitbucket.org",
}
