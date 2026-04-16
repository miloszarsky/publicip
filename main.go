package main

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

type Config struct {
	Port           string
	BindAddr       string
	Domain         string
	Title          string
	TrustedProxies []*net.IPNet
}

type IPInfo struct {
	IP      string `json:"ip"`
	Version string `json:"version"`
}

type templateData struct {
	Title  string
	Domain string
}

func loadConfig() Config {
	cfg := Config{
		Port:     envOrDefault("PORT", "3000"),
		BindAddr: envOrDefault("BIND_ADDR", "0.0.0.0"),
		Domain:   envOrDefault("DOMAIN", "localhost"),
		Title:    envOrDefault("TITLE", "public ip"),
	}

	if raw := os.Getenv("TRUSTED_PROXIES"); raw != "" {
		for _, cidr := range strings.Split(raw, ",") {
			cidr = strings.TrimSpace(cidr)
			if !strings.Contains(cidr, "/") {
				if strings.Contains(cidr, ":") {
					cidr += "/128"
				} else {
					cidr += "/32"
				}
			}
			_, network, err := net.ParseCIDR(cidr)
			if err != nil {
				slog.Warn("ignoring invalid TRUSTED_PROXIES entry", "cidr", cidr, "error", err)
				continue
			}
			cfg.TrustedProxies = append(cfg.TrustedProxies, network)
		}
	}

	return cfg
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func (cfg *Config) isTrustedProxy(ip string) bool {
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return false
	}
	for _, network := range cfg.TrustedProxies {
		if network.Contains(parsed) {
			return true
		}
	}
	return false
}

func (cfg *Config) getClientIP(r *http.Request) string {
	remoteHost, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		remoteHost = r.RemoteAddr
	}

	// Only trust proxy headers if the direct connection is from a trusted proxy
	if cfg.isTrustedProxy(remoteHost) {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			// Walk the chain right-to-left, return the first non-trusted IP
			parts := strings.Split(xff, ",")
			for i := len(parts) - 1; i >= 0; i-- {
				ip := strings.TrimSpace(parts[i])
				if ip != "" && !cfg.isTrustedProxy(ip) {
					return ip
				}
			}
		}
		if xri := r.Header.Get("X-Real-IP"); xri != "" {
			return strings.TrimSpace(xri)
		}
	}

	return remoteHost
}

func ipVersion(ip string) string {
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return "unknown"
	}
	if parsed.To4() != nil {
		return "IPv4"
	}
	return "IPv6"
}

func isCLI(r *http.Request) bool {
	ua := strings.ToLower(r.Header.Get("User-Agent"))
	for _, prefix := range []string{"curl", "wget", "httpie"} {
		if strings.HasPrefix(ua, prefix) {
			return true
		}
	}
	return false
}

func newMux(cfg *Config, tmpl *template.Template) *http.ServeMux {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}

		clientIP := cfg.getClientIP(r)
		version := ipVersion(clientIP)

		if isCLI(r) {
			accept := r.Header.Get("Accept")
			if strings.Contains(accept, "application/json") {
				writeJSON(w, IPInfo{IP: clientIP, Version: version})
				return
			}
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			fmt.Fprintf(w, "%s\n", clientIP)
			return
		}

		if strings.Contains(r.Header.Get("Accept"), "application/json") {
			writeJSON(w, IPInfo{IP: clientIP, Version: version})
			return
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		tmpl.Execute(w, templateData{Title: cfg.Title, Domain: cfg.Domain})
	})

	mux.HandleFunc("GET /api", func(w http.ResponseWriter, r *http.Request) {
		clientIP := cfg.getClientIP(r)
		writeJSON(w, IPInfo{IP: clientIP, Version: ipVersion(clientIP)})
	})

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "ok")
	})

	return mux
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(v)
}

func main() {
	// Self health check mode for scratch containers (no shell/wget/curl available)
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		port := envOrDefault("PORT", "3000")
		resp, err := http.Get("http://127.0.0.1:" + port + "/healthz")
		if err != nil || resp.StatusCode != http.StatusOK {
			os.Exit(1)
		}
		os.Exit(0)
	}

	cfg := loadConfig()

	tmpl := template.Must(template.New("index").Parse(htmlTemplate))

	addr := net.JoinHostPort(cfg.BindAddr, cfg.Port)
	srv := &http.Server{
		Addr:         addr,
		Handler:      newMux(&cfg, tmpl),
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	// Graceful shutdown
	done := make(chan os.Signal, 1)
	signal.Notify(done, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		slog.Info("server starting", "addr", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("server failed", "error", err)
			os.Exit(1)
		}
	}()

	<-done
	slog.Info("shutting down")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		slog.Error("forced shutdown", "error", err)
	}
}

const htmlTemplate = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}}</title>
<style>
  *, *::before, *::after { box-sizing: border-box; margin: 0; padding: 0; }

  :root {
    --bg: #0f0f13;
    --card: #1a1a24;
    --border: #2a2a3a;
    --text: #e4e4ef;
    --muted: #8888a0;
    --accent: #6c63ff;
    --accent-glow: rgba(108, 99, 255, .25);
    --green: #22c55e;
    --blue: #3b82f6;
    --radius: 16px;
  }

  body {
    font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, "Helvetica Neue", Arial, sans-serif;
    background: var(--bg);
    color: var(--text);
    min-height: 100vh;
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    padding: 2rem;
  }

  h1 {
    font-size: 1.5rem;
    font-weight: 600;
    letter-spacing: -.02em;
    margin-bottom: 2rem;
    color: var(--muted);
  }
  h1 span { color: var(--accent); }

  .card {
    background: var(--card);
    border: 1px solid var(--border);
    border-radius: var(--radius);
    padding: 2.5rem 3rem;
    text-align: center;
    min-width: 360px;
    max-width: 520px;
    width: 100%;
    box-shadow: 0 0 60px var(--accent-glow);
    animation: fadeUp .5s ease;
  }

  @keyframes fadeUp {
    from { opacity: 0; transform: translateY(12px); }
    to   { opacity: 1; transform: translateY(0); }
  }

  .ip {
    font-size: clamp(1.2rem, 5vw, 2.2rem);
    font-weight: 700;
    letter-spacing: .01em;
    word-break: break-all;
    margin: .75rem 0;
    color: #fff;
    user-select: all;
  }

  .badge {
    display: inline-block;
    padding: .25rem .75rem;
    border-radius: 999px;
    font-size: .8rem;
    font-weight: 600;
    text-transform: uppercase;
    letter-spacing: .06em;
  }
  .badge.ipv4 { background: rgba(34,197,94,.15); color: var(--green); }
  .badge.ipv6 { background: rgba(59,130,246,.15); color: var(--blue); }

  .copy-btn {
    margin-top: 1.5rem;
    padding: .6rem 1.5rem;
    border: 1px solid var(--border);
    border-radius: 8px;
    background: transparent;
    color: var(--muted);
    font-size: .85rem;
    cursor: pointer;
    transition: all .2s;
  }
  .copy-btn:hover { border-color: var(--accent); color: var(--accent); }

  .divider {
    width: 60px;
    height: 1px;
    background: var(--border);
    margin: 2rem auto;
  }

  .cli {
    background: #12121a;
    border: 1px solid var(--border);
    border-radius: 10px;
    padding: 1rem 1.25rem;
    text-align: left;
    font-family: "SF Mono", "Fira Code", "Cascadia Code", monospace;
    font-size: .82rem;
    color: var(--muted);
    position: relative;
    overflow-x: auto;
  }
  .cli code { color: var(--green); }
  .cli .comment { color: #555; }

  footer {
    margin-top: 2.5rem;
    font-size: .75rem;
    color: var(--muted);
  }

  .loading .ip { animation: pulse 1.2s ease infinite; }
  @keyframes pulse {
    0%, 100% { opacity: .3; }
    50%      { opacity: .7; }
  }
</style>
</head>
<body>
  <h1><span>&gt;</span> {{.Title}}</h1>

  <div class="card loading" id="card">
    <span class="badge" id="badge">detecting...</span>
    <div class="ip" id="ip">...</div>
    <button class="copy-btn" id="copy" onclick="copyIP()">Copy to clipboard</button>
  </div>

  <div class="divider"></div>

  <div class="cli">
    <div><span class="comment"># plain text</span></div>
    <div>$ <code>curl -L {{.Domain}}</code></div>
    <br>
    <div><span class="comment"># json</span></div>
    <div>$ <code>curl -L -H "Accept: application/json" {{.Domain}}</code></div>
  </div>

  <footer>Lightweight &middot; IPv4 + IPv6 &middot; No tracking</footer>

<script>
  async function fetchIP() {
    try {
      const res = await fetch("/api");
      const data = await res.json();
      document.getElementById("ip").textContent = data.ip;
      const badge = document.getElementById("badge");
      badge.textContent = data.version;
      badge.className = "badge " + data.version.toLowerCase();
      document.getElementById("card").classList.remove("loading");
    } catch (e) {
      document.getElementById("ip").textContent = "unable to detect";
    }
  }

  function copyIP() {
    const ip = document.getElementById("ip").textContent;
    navigator.clipboard.writeText(ip).then(function() {
      var btn = document.getElementById("copy");
      btn.textContent = "Copied!";
      setTimeout(function() { btn.textContent = "Copy to clipboard"; }, 1500);
    });
  }

  fetchIP();
</script>
</body>
</html>
`
