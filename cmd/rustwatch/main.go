// RustWatch — a desktop Rust+ companion bot: team tracking, map events, vending search,
// smart devices, team-chat commands and a BattleMetrics player tracker.
package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"rustwatch/internal/app"
	"rustwatch/internal/fakerust"
)

const defaultPort = 47815

func main() {
	demo := flag.Bool("demo", false, "run against a built-in simulated server (no Rust needed)")
	noWindow := flag.Bool("no-window", false, "don't open the app window")
	port := flag.Int("port", defaultPort, "local UI port")
	dataFlag := flag.String("data", "", "use this folder for settings and logs")
	afterRestart := flag.Bool("after-restart", false, "internal: started by a restart")
	flag.Parse()

	var dir string
	switch {
	case *demo:
		dir = filepath.Join(os.TempDir(), "RustWatchDemo")
		os.RemoveAll(dir)
	case *dataFlag != "":
		dir = *dataFlag
	default:
		dir = app.DataDir()
	}
	os.MkdirAll(filepath.Join(dir, "logs"), 0o755)
	log.SetOutput(app.AppLog(dir))
	log.Printf("RustWatch %s starting, data folder %s", app.Version, dir)
	if *afterRestart {
		time.Sleep(1500 * time.Millisecond) // let the previous instance release the port
	}

	// single instance: if already running, just open another window
	if !*demo {
		c := &http.Client{Timeout: time.Second}
		if r, err := c.Get(fmt.Sprintf("http://127.0.0.1:%d/api/ping", *port)); err == nil {
			r.Body.Close()
			if r.StatusCode == 200 {
				c.Post(fmt.Sprintf("http://127.0.0.1:%d/api/window", *port), "application/json", nil)
				return
			}
		}
	}

	app.CleanupOldExe() // remove the previous version left by an update
	a := app.New(dir)
	l, err := app.Listen(*port)
	if err != nil {
		log.Fatal(err)
	}
	go func() { log.Println(a.Serve(l)) }()
	for a.Port == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	if *demo {
		a.Demo = true
		fs := fakerust.New()
		if err := fs.Start("127.0.0.1:0"); err != nil {
			log.Fatal(err)
		}
		bm, _ := net.Listen("tcp", "127.0.0.1:0")
		go http.Serve(bm, fs.BattleMetrics())
		a.SetBMBaseURL("http://" + bm.Addr().String())
		stats, _ := net.Listen("tcp", "127.0.0.1:0")
		go http.Serve(stats, fs.SteamStats())
		a.DemoSetup("http://" + stats.Addr().String())
		qaddr, _ := fs.StartQuery("127.0.0.1:0")
		fs.CensorNames = true                   // like most real servers: the public list has stand-in names…
		a.DemoLocalSteam(fs.OnlineSteam, qaddr) // …but the Steam client knows who's really there
		a.DemoAudio()
		host, p, _ := net.SplitHostPort(fs.Addr)
		pn, _ := strconv.Atoi(p)
		a.AddServer(app.ServerConfig{Name: "Demo server", IP: host, Port: pn, PlayerID: "76561198000000001", PlayerToken: 1234, QueryHint: qaddr,
			Devices: []app.Device{
				{EntityID: 1001, Type: 1, Name: "Turrets", Command: "turrets"},
				{EntityID: 1002, Type: 1, Name: "Lights", Command: "lights"},
				{EntityID: 2001, Type: 2, Name: "Base alarm"},
				{EntityID: 3001, Type: 3, Name: "Main TC"},
			},
			Watches: []app.VendingWatch{{ItemID: 1545779598}},
			Tracked: []app.TrackedPlayer{{BMID: "1004", Name: "night_raider", Tag: "Enemy", Color: "#e5484d", Notify: true},
				{BMID: "1003", Name: "Gary the Builder", Tag: "Neighbour", Color: "#e8a33d", Notify: true},
				{Name: "zerg_lord", Tag: "Zerg", Color: "#8e4ec6", Notify: true},
				{SteamID: "76561198000000077", Name: "Ghost", Tag: "Steam", Color: "#3e8ed0", Notify: true},
				{SteamID: "76561198000000078", Name: "Hermit", Tag: "Steam", Color: "#3e8ed0"}},
		})
		a.DemoSeed()
	}
	a.Start()
	if !*noWindow {
		a.OpenWindow()
	}
	fmt.Printf("RustWatch running at http://127.0.0.1:%d/\n", a.Port)
	<-a.Quit()
}
