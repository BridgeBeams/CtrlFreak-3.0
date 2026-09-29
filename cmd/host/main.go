// Command host is the CtrlFreak agent installed on a machine you want to control.
//
// You normally do NOT run this from a command line. The Windows installer
// (see installer/) collects the relay address, your username, and password,
// writes them next to this program as ctrlfreak-host.json, installs a small
// SYSTEM supervisor service, and registers a logon scheduled task that runs the
// agent elevated in your desktop session. After that it just runs on every boot
// and heals itself if it stops.
//
// Architecture (v3): two roles, one exe.
//
//   - SUPERVISOR (mode=service): installed as the "CtrlFreakSvc" Windows service,
//     runs as SYSTEM, always on. It does not touch the screen. Its only job is to
//     keep the helper alive: it watches a heartbeat file the helper writes and,
//     if the helper crashes or is told to restart, re-launches the logon task
//     within a few seconds.
//   - HELPER (mode=helper): the actual agent. Launched by a logon scheduled task
//     ("CtrlFreak Helper") so Windows runs it ELEVATED and inside your interactive
//     desktop session. That is what lets it capture the real screen and inject
//     mouse/keyboard. It connects outbound to the relay and serves sessions.
//
// Running it with no mode (e.g. double-clicking) just runs the helper in the
// foreground, so manual testing still works.
//
// Service control verbs:
//
//	ctrlfreak-host.exe -service install     # register + start the supervisor
//	ctrlfreak-host.exe -service uninstall   # stop + remove the supervisor
//	ctrlfreak-host.exe -service start|stop|status
//
// Settings resolution order (highest wins): command-line flags, then
// ctrlfreak-host.json next to the exe (or -config path), then CTRLFREAK_PASS env.
package main

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/kardianos/service"
	"github.com/wthomson/ctrlfreak/internal/host"
)

// serviceName is the SYSTEM supervisor service. It is intentionally different
// from any earlier name so a v3 install does not collide with older attempts.
const serviceName = "CtrlFreakSvc"

// fileConfig is the on-disk settings file (ctrlfreak-host.json) that the
// installer writes and both roles read.
type fileConfig struct {
	Relay  string `json:"relay"`  // wss://your-relay/ws
	User   string `json:"user"`   // CtrlFreak username
	Pass   string `json:"pass"`   // CtrlFreak password
	Name   string `json:"name"`   // friendly machine name
	Verify bool   `json:"verify"` // verify relay TLS cert (true once relay has a real cert)
}

func main() {
	var (
		relay   = flag.String("relay", "", "relay signaling URL, e.g. wss://host/ws")
		user    = flag.String("user", "", "CtrlFreak username")
		pass    = flag.String("pass", "", "CtrlFreak password (or CTRLFREAK_PASS)")
		name    = flag.String("name", "", "friendly name for this machine (default: hostname)")
		verify  = flag.Bool("verify", false, "verify the relay's TLS certificate")
		cfgFlag = flag.String("config", "", "path to ctrlfreak-host.json (default: next to the exe)")
		mode    = flag.String("mode", "", "internal: 'helper' (agent) or 'service' (supervisor)")
		svcCtl  = flag.String("service", "", "service control: install | uninstall | start | stop | status")
	)
	flag.Parse()

	cfgPath := resolveConfigPath(*cfgFlag)
	cfg := loadFileConfig(cfgPath)

	// Flags override the file.
	if *relay != "" {
		cfg.Relay = *relay
	}
	if *user != "" {
		cfg.User = *user
	}
	if *pass != "" {
		cfg.Pass = *pass
	}
	if *name != "" {
		cfg.Name = *name
	}
	if *verify {
		cfg.Verify = true
	}
	if cfg.Pass == "" {
		cfg.Pass = os.Getenv("CTRLFREAK_PASS")
	}
	if cfg.Name == "" {
		if hn, err := os.Hostname(); err == nil {
			cfg.Name = hn
		} else {
			cfg.Name = "unnamed-host"
		}
	}

	// ---- Service control verbs -------------------------------------------
	if *svcCtl != "" {
		runServiceControl(*svcCtl, cfg, cfgPath)
		return
	}

	// ---- Helper (the actual agent) ---------------------------------------
	if *mode == "helper" {
		host.InitLog("helper")
		log.Printf("CtrlFreak helper starting (pid %d)", os.Getpid())
		if cfg.Relay == "" || cfg.User == "" || cfg.Pass == "" {
			log.Printf("not configured (relay/user/pass missing in %s); nothing to do", cfgPath)
			return
		}
		stop := make(chan struct{})
		go host.RunHeartbeat(stop)
		runHelper(cfg)
		return
	}

	// ---- Supervisor (installed service) ----------------------------------
	if *mode == "service" {
		host.InitLog("service")
		prg := &program{cfg: cfg, cfgPath: cfgPath}
		svc, err := service.New(prg, supervisorConfig(cfgPath))
		if err != nil {
			log.Fatalf("service init: %v", err)
		}
		if err := svc.Run(); err != nil {
			log.Printf("supervisor run: %v", err)
		}
		return
	}

	// ---- No mode: interactive/manual run ---------------------------------
	// Double-clicked or run from a console with a config: behave like the helper
	// in the foreground so manual use and testing still work.
	if cfg.Relay == "" || cfg.User == "" || cfg.Pass == "" {
		fmt.Println("CtrlFreak host is not configured yet.")
		fmt.Println("Run the installer, or create ctrlfreak-host.json next to this program:")
		fmt.Println(`  { "relay": "wss://your-relay/ws", "user": "wade", "pass": "...", "name": "Work PC" }`)
		return
	}
	host.InitLog("helper")
	log.Printf("CtrlFreak helper starting (manual/foreground, pid %d)", os.Getpid())
	stop := make(chan struct{})
	go host.RunHeartbeat(stop)
	runHelper(cfg)
}

// supervisorConfig is the kardianos service definition. The SCM launches the
// exe with -mode service so main() runs the supervisor branch.
func supervisorConfig(cfgPath string) *service.Config {
	return &service.Config{
		Name:        serviceName,
		DisplayName: "CtrlFreak Supervisor",
		Description: "Keeps the CtrlFreak remote-control agent running in your session.",
		Arguments:   []string{"-mode", "service", "-config", cfgPath},
	}
}

// program implements service.Interface for the supervisor.
type program struct {
	cfg     fileConfig
	cfgPath string
	stop    chan struct{}
}

func (p *program) Start(s service.Service) error {
	p.stop = make(chan struct{})
	go p.run()
	return nil
}

func (p *program) Stop(s service.Service) error {
	if p.stop != nil {
		close(p.stop)
	}
	return nil
}

// run is the supervisor loop. On Windows it keeps the logon helper task alive.
// On other platforms (dev) it simply runs the agent inline.
func (p *program) run() {
	host.VerifyRelayCert = p.cfg.Verify
	if runtime.GOOS != "windows" {
		go host.RunHeartbeat(p.stop)
		runHelper(p.cfg)
		return
	}
	log.Printf("CtrlFreak supervisor starting (pid %d)", os.Getpid())
	host.SuperviseHelper(p.stop)
}

// ---- Helper: login loop + serve ----------------------------------------

// runHelper logs in and serves sessions, re-logging in on every reconnect so a
// long-running agent keeps working past the token's expiry. Blocks until the
// process exits.
func runHelper(cfg fileConfig) {
	host.VerifyRelayCert = cfg.Verify
	agent := host.NewAgent(cfg.Relay, "", cfg.Name, runtime.GOOS, host.NewCapturer(), host.NewInjector())
	for {
		token, err := login(cfg)
		if err != nil {
			log.Printf("login failed: %v; retrying in 15s", err)
			time.Sleep(15 * time.Second)
			continue
		}
		agent.SetToken(token)
		log.Printf("connected to relay as %s", cfg.User)
		if err := agent.Serve(); err != nil {
			log.Printf("relay connection lost: %v", err)
		}
		time.Sleep(5 * time.Second)
	}
}

// runServiceControl installs/uninstalls/starts/stops/queries the supervisor.
func runServiceControl(verb string, cfg fileConfig, cfgPath string) {
	prg := &program{cfg: cfg, cfgPath: cfgPath}
	svc, err := service.New(prg, supervisorConfig(cfgPath))
	if err != nil {
		log.Fatalf("service init: %v", err)
	}
	if verb == "install" {
		if cfg.Relay == "" || cfg.User == "" || cfg.Pass == "" {
			log.Fatal("cannot install: relay, user, and pass must be set " +
				"(via flags or ctrlfreak-host.json next to the exe)")
		}
		if err := saveFileConfig(cfgPath, cfg); err != nil {
			log.Fatalf("write config: %v", err)
		}
	}
	if err := service.Control(svc, verb); err != nil {
		log.Fatalf("service %s: %v", verb, err)
	}
	if verb == "install" {
		// If the supervisor itself ever crashes, let the SCM restart it.
		configureServiceRecovery(serviceName)
	}
	fmt.Printf("service %s: ok\n", verb)
}

// login exchanges username/password for a token via the relay's HTTPS API.
func login(cfg fileConfig) (string, error) {
	u, err := url.Parse(cfg.Relay)
	if err != nil {
		return "", err
	}
	switch u.Scheme {
	case "wss":
		u.Scheme = "https"
	case "ws":
		u.Scheme = "http"
	}
	u.Path = "/api/login"
	u.RawQuery = ""

	body, _ := json.Marshal(map[string]string{"username": cfg.User, "password": cfg.Pass})
	client := &http.Client{Timeout: 20 * time.Second}
	if !cfg.Verify {
		client.Transport = &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}} // #nosec G402
	}
	resp, err := client.Post(u.String(), "application/json", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(msg)))
	}
	var out struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	return out.Token, nil
}

func resolveConfigPath(flagPath string) string {
	if flagPath != "" {
		abs, err := filepath.Abs(flagPath)
		if err == nil {
			return abs
		}
		return flagPath
	}
	exe, err := os.Executable()
	if err != nil {
		return "ctrlfreak-host.json"
	}
	return filepath.Join(filepath.Dir(exe), "ctrlfreak-host.json")
}

func loadFileConfig(path string) fileConfig {
	var cfg fileConfig
	data, err := os.ReadFile(path)
	if err != nil {
		return cfg
	}
	_ = json.Unmarshal(data, &cfg)
	return cfg
}

func saveFileConfig(path string, cfg fileConfig) error {
	data, _ := json.MarshalIndent(cfg, "", "  ")
	return os.WriteFile(path, data, 0o600)
}

// configureServiceRecovery tells the Windows SCM to restart the supervisor if it
// exits. No-op off Windows.
func configureServiceRecovery(name string) {
	if runtime.GOOS != "windows" {
		return
	}
	cmd := exec.Command("sc", "failure", name, "reset=", "60",
		"actions=", "restart/5000/restart/5000/restart/5000")
	if out, err := cmd.CombinedOutput(); err != nil {
		log.Printf("note: could not set service recovery (%v): %s", err, strings.TrimSpace(string(out)))
	}
}
