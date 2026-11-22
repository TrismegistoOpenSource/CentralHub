# Componenti di terze parti

CentralHub è distribuito sotto [licenza MIT](LICENSE). Il binario compilato
incorpora staticamente le librerie Go elencate qui sotto, ciascuna sotto la
propria licenza permissiva (MIT, BSD, ISC, Apache-2.0: nessuna copyleft,
tutte compatibili con la MIT). Del sistema usa soltanto le librerie che ci
sono già (WebKit su macOS, GTK e WebKitGTK su Linux, WebView2 su Windows):
non vengono distribuite con l'app.

I testi di licenza stanno **dentro il programma**, senza cartelle accanto: si leggono con il clic sul numero di versione (su macOS anche dal
menu «Informazioni su CentralHub e licenze…»), in gruppi chiusi con una riga per componente e il testo intero, anche senza rete. Contengono
la licenza MIT, questo file, i testi in `licenses/` e quelli di **ogni modulo Go** compilato nel binario (elenco in `moduli-go/ELENCO.txt`),
raccolti da `scripts/raccogli-licenze.sh` a ogni build e incorporati nell'eseguibile (`internal/licenze`); `scripts/verifica-pacchetto.sh`
controlla che ci siano tutti.

## Dipendenze dirette

| Componente | Licenza | Copyright / origine |
|---|---|---|
| [Wails v2](https://wails.io) | MIT | © Lea Anthony |
| Go standard library e runtime | BSD-3-Clause | © The Go Authors |

## Dipendenze indirette (portate da Wails)

| Componente | Licenza |
|---|---|
| labstack/echo, labstack/gommon | MIT |
| gorilla/websocket | BSD-2-Clause |
| go-ole/go-ole | MIT |
| godbus/dbus | BSD-2-Clause |
| google/uuid | BSD-3-Clause |
| jchv/go-winloader | ISC |
| leaanthony/go-ansi-parser, gosod, slicer, u | MIT |
| mattn/go-colorable, go-isatty | MIT |
| pkg/browser, pkg/errors | BSD-2-Clause |
| rivo/uniseg | MIT |
| samber/lo | MIT |
| tkrajina/go-reflector | Apache-2.0 |
| valyala/bytebufferpool, fasttemplate | MIT |
| wailsapp/go-webview2, wailsapp/mimetype | MIT |
| bep/debounce | MIT |
| jackmordaunt/go-toast | MIT |
| golang.org/x/crypto, x/net, x/sys, x/text | BSD-3-Clause |

## Solo nel pacchetto Linux

| Componente | Licenza | Uso |
|---|---|---|
| [Runtime di AppImage](https://github.com/AppImage/type2-runtime), con musl, libfuse, squashfuse, Zstandard e zlib collegate staticamente | MIT, LGPL-2.1 (libfuse), BSD, zlib | programma separato che apre l'AppImage; indirizzi del sorgente e testi: `licenses/appimage-runtime.txt` |

## Codice di altri autori dentro i moduli (licenze annidate)

Alcuni moduli contengono, in una sottocartella, pacchetti di altri autori con
una licenza propria. Finiscono nel binario, quindi i loro testi stanno dentro
il programma (raccolta `moduli-go/<modulo>/annidate/`) (li raccoglie
`scripts/raccogli-licenze.sh`, e `scripts/verifica-pacchetto.sh` controlla che
ci siano):

| Pacchetto | Licenza | Copyright | Dove finisce |
|---|---|---|---|
| `wails/v2/internal/frontend/desktop/windows/winc` | MIT | © 2019 winc Authors | solo Windows |
| `wails/v2/internal/go-common-file-dialog` | MIT | © 2019 Harry Phillips | solo Windows |
| `go-webview2/webviewloader` | ISC | © 2020 John Chadwick, © 2022 Wails Project Developers | solo Windows |
| `wails/v2/internal/typescriptify` | Apache-2.0 | © 2015 Tomo Krajina | tutti i sistemi |

A parte il runtime dell'AppImage, CentralHub non porta con sé nessun programma esterno: usa quelli del sistema
(`ditto` su macOS, PowerShell su Windows, `pkexec` su Linux quando si
installa per tutto il computer).
