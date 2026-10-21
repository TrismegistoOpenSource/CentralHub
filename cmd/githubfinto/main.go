// SPDX-License-Identifier: MIT

// githubfinto è un piccolo GitHub di prova, per provare CentralHub su un
// computer vero senza token e senza toccare i repository veri. Non fa parte
// dell'app distribuita.
//
// Serve le release da una cartella fatta così:
//
//	<radice>/<proprietario>/<repo>/<tag>/<file della release>
//
// L'ultima release è il tag con la versione più alta; l'elenco delle release
// le dà tutte, dalla più recente. Uso:
//
//	go run ./cmd/githubfinto -radice /percorso/prove -porta 8765
//
// e CentralHub va lanciato in modalità di prova, puntato qui:
//
//	CENTRALHUB_CONFIG_DIR=/percorso/config CENTRALHUB_API=http://127.0.0.1:8765 ./CentralHub
//
// Con -privati i repository dei proprietari indicati si comportano come
// repository privati: ognuno vuole il proprio token, che è
// "github_pat_<segreto>_<nome del repository in minuscolo>" (così si prova
// che ogni repository usa il suo). Con -lento i download rallentano (kB al
// secondo), per vedere la barra di avanzamento e provare l'interruzione.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"centralhub/internal/github"
	"centralhub/internal/scarica"
)

func main() {
	radice := flag.String("radice", ".", "cartella con <proprietario>/<repo>/<tag>/<file>")
	porta := flag.Int("porta", 8765, "porta su 127.0.0.1")
	privati := flag.String("privati", "", "proprietari i cui repository sono privati, separati da virgola")
	segreto := flag.String("segreto", "prova", "parte comune dei token dei repository privati")
	lento := flag.Int("lento", 0, "kB al secondo per i download (0 = senza limite)")
	flag.Parse()
	base := fmt.Sprintf("http://127.0.0.1:%d", *porta)

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		log.Printf("%s %s", r.Method, r.URL.Path)
		parti := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		// repos/<proprietario>/<repo>/… e file/<proprietario>/<repo>/…
		if len(parti) >= 3 {
			for _, p := range strings.Split(*privati, ",") {
				atteso := "Bearer github_pat_" + *segreto + "_" + strings.ToLower(parti[2])
				if p != "" && strings.EqualFold(p, parti[1]) && r.Header.Get("Authorization") != atteso {
					w.WriteHeader(http.StatusNotFound) // GitHub nasconde i privati così
					fmt.Fprint(w, `{"message":"Not Found"}`)
					return
				}
			}
		}
		switch {
		// /repos/<proprietario>/<repo>: esiste?
		case len(parti) == 3 && parti[0] == "repos" && r.Method == http.MethodGet:
			if fi, err := os.Stat(filepath.Join(*radice, parti[1], parti[2])); err != nil || !fi.IsDir() {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			fmt.Fprintf(w, `{"full_name":%q}`, parti[1]+"/"+parti[2])
		// qualunque scrittura: i token di prova sono in sola lettura
		case r.Method != http.MethodGet:
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, `{"message":"Resource not accessible by personal access token"}`)
		// /repos/<proprietario>/<repo>/releases/latest e /repos/<proprietario>/<repo>/releases
		case (len(parti) == 5 && parti[0] == "repos" && parti[3] == "releases" && parti[4] == "latest") ||
			(len(parti) == 4 && parti[0] == "repos" && parti[3] == "releases"):
			dir := filepath.Join(*radice, parti[1], parti[2])
			tag := scarica.Scaricate(dir)
			if len(tag) == 0 {
				w.WriteHeader(http.StatusNotFound)
				fmt.Fprint(w, `{"message":"Not Found"}`)
				return
			}
			var tutte []github.Release
			for _, t := range tag {
				rel := github.Release{Tag: t, Nome: "Release " + t}
				if fi, err := os.Stat(filepath.Join(dir, t)); err == nil {
					rel.Pubblicata = fi.ModTime().Format(time.RFC3339)
				}
				voci, _ := os.ReadDir(filepath.Join(dir, t))
				for _, v := range voci {
					if fi, err := v.Info(); err == nil && !v.IsDir() && !strings.HasPrefix(v.Name(), ".") {
						// come GitHub, che dà il digest di ogni file: senza, CentralHub non installa
						rel.Assets = append(rel.Assets, github.Asset{Nome: v.Name(), Dimensione: fi.Size(), Digest: sommaDi(filepath.Join(dir, t, v.Name())),
							URL: base + "/file/" + parti[1] + "/" + parti[2] + "/" + t + "/" + v.Name()})
					}
				}
				tutte = append(tutte, rel)
			}
			if len(parti) == 5 {
				json.NewEncoder(w).Encode(tutte[0])
			} else {
				json.NewEncoder(w).Encode(tutte)
			}
		// /file/<proprietario>/<repo>/<tag>/<nome>
		case len(parti) == 5 && parti[0] == "file":
			f, err := os.Open(filepath.Join(*radice, parti[1], parti[2], parti[3], filepath.Base(parti[4])))
			if err != nil {
				http.NotFound(w, r)
				return
			}
			defer f.Close()
			if fi, err := f.Stat(); err == nil {
				w.Header().Set("Content-Length", fmt.Sprint(fi.Size()))
			}
			if *lento <= 0 {
				io.Copy(w, f)
				return
			}
			blocco := make([]byte, *lento*1024/10)
			for {
				n, err := f.Read(blocco)
				if n > 0 {
					if _, werr := w.Write(blocco[:n]); werr != nil {
						return
					}
					if fl, ok := w.(http.Flusher); ok {
						fl.Flush()
					}
					time.Sleep(100 * time.Millisecond)
				}
				if err != nil {
					return
				}
			}
		default:
			http.NotFound(w, r)
		}
	})
	log.Printf("GitHub finto su %s, release da %s", base, *radice)
	log.Fatal(http.ListenAndServe(fmt.Sprintf("127.0.0.1:%d", *porta), nil))
}

// sommaDi è il digest di un file nella forma di GitHub ("sha256:<esadecimale>").
func sommaDi(percorso string) string {
	f, err := os.Open(percorso)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return ""
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}
