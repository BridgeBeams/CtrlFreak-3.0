// Command relay is the CtrlFreak rendezvous server ("hub"). Every host agent and
// every controller connects OUTBOUND to this one process, which is how CtrlFreak
// crosses NAT and corporate firewalls without inbound port forwarding on the
// controlled machines.
//
// It can run in the foreground (double-click the exe) or as an auto-starting
// Windows service (the unified installer chooses this for the Hub component):
//
//	ctrlfreak-relay.exe -service install     # register + start the service
//	ctrlfreak-relay.exe -service uninstall
//
// Paths (database, cert, jwt secret) are resolved next to the exe unless given as
// absolute paths, so it behaves the same whether launched by hand or by the
// service manager (whose working directory is elsewhere).
package main

import (
	"embed"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/kardianos/service"
	"github.com/wthomson/ctrlfreak/internal/relay"
	"golang.org/x/term"
)

// The browser controller is compiled into the binary so the relay is a single
// self-contained file you can drop on any machine.
//
//go:embed all:web
var webFiles embed.FS

func main() {
	cfgFlag := flag.String("config", "", "path to JSON config file (default: next to the exe)")
	svcCtl := flag.String("service", "", "service control: install | uninstall | start | stop | status")
	flag.Parse()

	exeDir := executableDir()
	cfgPath := *cfgFlag
	if cfgPath == "" {
		cfgPath = filepath.Join(exeDir, "config.json")
	} else if abs, err := filepath.Abs(cfgPath); err == nil {
		cfgPath = abs
	}

	cfg, err := relay.LoadConfig(cfgPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	// Environment overrides win over the file.
	if v := os.Getenv("CTRLFREAK_ADMIN_PASS"); v != "" {
		cfg.AdminPass = v
	}
	if v := os.Getenv("CTRLFREAK_JWT_SECRET"); v != "" {
		cfg.JWTSecret = v
	}
	if v := os.Getenv("CTRLFREAK_ADDR"); v != "" {
		cfg.Addr = v
	}
	// Resolve data paths next to the exe so a service (whose CWD is system32)
	// finds the same files a foreground launch does.
	cfg.DBPath = resolveRelative(exeDir, cfg.DBPath, "ctrlfreak.db")
	if cfg.TLSCert != "" {
		cfg.TLSCert = resolveRelative(exeDir, cfg.TLSCert, "")
	}
	if cfg.TLSKey != "" {
		cfg.TLSKey = resolveRelative(exeDir, cfg.TLSKey, "")
	}

	svcConfig := &service.Config{
		Name:        "CtrlFreakRelay",
		DisplayName: "CtrlFreak Hub (relay)",
		Description: "CtrlFreak rendezvous server and web controller.",
		Arguments:   []string{"-config", cfgPath},
	}
	prg := &program{cfg: cfg, exeDir: exeDir}
	svc, err := service.New(prg, svcConfig)
	if err != nil {
		log.Fatalf("service init: %v", err)
	}

	if *svcCtl != "" {
		if *svcCtl == "install" && cfg.AdminPass == "" && !adminExists(cfg) {
			log.Fatal("cannot install the hub yet: set admin_pass in config.json " +
				"(the installer does this) so the first admin can be created on boot")
		}
		if err := service.Control(svc, *svcCtl); err != nil {
			log.Fatalf("service %s: %v", *svcCtl, err)
		}
		if *svcCtl == "install" {
			configureServiceRecovery(svcConfig.Name)
		}
		fmt.Printf("service %s: ok\n", *svcCtl)
		return
	}

	if err := svc.Run(); err != nil {
		log.Printf("run: %v", err)
	}
}

// program implements service.Interface for the relay.
type program struct {
	cfg    relay.Config
	exeDir string
}

func (p *program) Start(s service.Service) error {
	go p.run()
	return nil
}

func (p *program) Stop(s service.Service) error { return nil }

func (p *program) run() {
	store, err := relay.OpenStore(p.cfg.DBPath)
	if err != nil {
		log.Fatalf("open store: %v", err)
	}

	// Persist a JWT secret next to the database so logins survive restarts even
	// when none was configured.
	if p.cfg.JWTSecret == "" {
		p.cfg.JWTSecret = loadOrCreateSecret(p.cfg.DBPath + ".secret")
	}

	// Ensure an admin exists.
	if p.cfg.AdminPass != "" {
		if err := store.EnsureAdmin(p.cfg.AdminUser, p.cfg.AdminPass); err != nil {
			log.Fatalf("ensure admin: %v", err)
		}
	} else if _, gerr := store.GetUser(p.cfg.AdminUser); gerr != nil {
		pass := promptFirstRunPassword(p.cfg.AdminUser)
		if pass == "" {
			log.Fatal("no admin exists yet. Launch the hub in a console to set the " +
				"first admin password, or set admin_pass in config.json.")
		}
		if err := store.EnsureAdmin(p.cfg.AdminUser, pass); err != nil {
			log.Fatalf("ensure admin: %v", err)
		}
	}

	sub, err := fs.Sub(webFiles, "web")
	if err != nil {
		log.Fatalf("web assets: %v", err)
	}
	srv := relay.NewServer(p.cfg, store, sub)
	log.Fatal(srv.Run())
}

// adminExists reports whether the configured admin already exists in the db.
func adminExists(cfg relay.Config) bool {
	store, err := relay.OpenStore(cfg.DBPath)
	if err != nil {
		return false
	}
	defer store.Close()
	_, err = store.GetUser(cfg.AdminUser)
	return err == nil
}

func executableDir() string {
	exe, err := os.Executable()
	if err != nil {
		return "."
	}
	return filepath.Dir(exe)
}

// resolveRelative returns p made absolute against base; if p is empty it uses
// def (also against base). Absolute p is returned unchanged.
func resolveRelative(base, p, def string) string {
	if p == "" {
		if def == "" {
			return ""
		}
		p = def
	}
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(base, p)
}

// loadOrCreateSecret reads a persisted secret or creates one.
func loadOrCreateSecret(path string) string {
	if b, err := os.ReadFile(path); err == nil && len(b) >= 32 {
		return string(b)
	}
	s := relay.NewRandomSecret()
	_ = os.WriteFile(path, []byte(s), 0o600)
	return s
}

// configureServiceRecovery asks Windows to restart the service if it exits.
func configureServiceRecovery(name string) {
	if runtime.GOOS != "windows" {
		return
	}
	cmd := exec.Command("sc", "failure", name, "reset=", "60",
		"actions=", "restart/5000/restart/5000/restart/5000")
	_ = cmd.Run()
}

// promptFirstRunPassword asks for the initial admin password when launched
// interactively and no admin exists yet. Input is masked. Returns "" if there is
// no console to prompt on.
func promptFirstRunPassword(user string) string {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return ""
	}
	fmt.Println("========================================")
	fmt.Println(" CtrlFreak first-time setup")
	fmt.Printf(" Set the admin password for user %q.\n", user)
	fmt.Println("========================================")
	for {
		fmt.Print("New admin password (min 8 chars): ")
		p1, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Println()
		if err != nil {
			return ""
		}
		if len(p1) < 8 {
			fmt.Println("Too short, try again.")
			continue
		}
		fmt.Print("Confirm password: ")
		p2, _ := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Println()
		if string(p1) != string(p2) {
			fmt.Println("Passwords did not match, try again.")
			continue
		}
		fmt.Println("Admin account created. Starting hub...")
		return string(p1)
	}
}
