# CentralHub 0.2.0

Tiene in ordine i programmi che arrivano da repository GitHub: **archivia**
le loro release e li **installa e aggiorna** sul computer. Scritto in Go +
[Wails](https://wails.io): leggero, usa la webview di sistema, nessuna
dipendenza da installare.

[![Build](https://github.com/TrismegistoOpenSource/CentralHub/actions/workflows/build.yml/badge.svg)](https://github.com/TrismegistoOpenSource/CentralHub/actions/workflows/build.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

## Download

Le build compilate (macOS Apple Silicon/Intel, Windows, Linux AppImage) sono
nella pagina **[Release](https://github.com/TrismegistoOpenSource/CentralHub/releases)**.

- **Windows**: si estrae lo zip e si lancia `CentralHub.exe`. Non si installa
  niente.
- **Linux**: si rende eseguibile l'AppImage e la si lancia.
- **macOS**: si estrae lo zip della propria architettura e si sposta l'app in
  Applicazioni.

### macOS: sbloccare l'app al primo avvio

L'app **non è firmata con un Apple Developer ID**, quindi macOS la mette in
quarantena e al primo avvio dice che è danneggiata o che non può essere aperta.
Non è danneggiata: è la quarantena.

Trascina prima l'app in **Applicazioni**, poi incolla nel Terminale la riga che
corrisponde alla versione scaricata:

```bash
xattr -dr com.apple.quarantine /Applications/CentralHub-AppleSilicon.app
```

```bash
xattr -dr com.apple.quarantine /Applications/CentralHub-Intel.app
```

Va fatto una volta sola, e serve solo per le app scaricate da internet. In
alternativa: clic destro sull'app → **Apri** → di nuovo **Apri**.

> Questa sezione esiste unicamente perché manca una firma Apple riconosciuta
> (che richiede un account Developer a pagamento). Il giorno in cui il progetto
> ne avrà una e le build saranno notarizzate, la quarantena non scatterà più e
> queste istruzioni andranno rimosse.

I programmi che CentralHub scarica e installa da sé **non** prendono la
quarantena: per quelli il passaggio non serve.

## Due modalità

### Archiviazione

Per ogni repository una cartella. A ogni release nuova CentralHub crea una
sottocartella con il nome del tag (`<cartella>/v1.2.0/`) e ci scarica **ogni
pacchetto come file separato**, senza estrarre né accorpare niente, più gli
archivi dei sorgenti.

- **Una release a metà non compare mai**: si scarica in una cartella
  provvisoria nascosta (`.<tag>.parziale`) che prende il nome definitivo solo
  a download completo e verificato (dimensione e, quando GitHub lo fornisce,
  SHA-256).
- **Un download interrotto riparte da dove era**: i file già arrivati interi
  restano nella cartella provvisoria e il tentativo successivo non li
  riscarica. Un file a metà non viene mai tenuto.
- **Una release già archiviata si salta.**
- Non installa niente.

### Update

Installa e aggiorna i programmi su questo computer: quelli **già
installati**, che rileva da solo, e quelli che si sceglie di **seguire**.
Anche la prima installazione passa da qui. Scarica solo il pacchetto adatto al
sistema, lo installa e lo toglie.

Alla prima installazione di un programma chiede dove metterlo; la scelta si
cambia da «Modifica».

| Sistema | Solo per me | Per tutto il computer |
|---|---|---|
| Linux | `~/App/<Nome>.AppImage` | `/opt/<Nome>/<Nome>.AppImage` |
| Windows | `%LOCALAPPDATA%\Programs\<Nome>\` | `%ProgramFiles%\<Nome>\` |
| macOS | `~/Applications/<Nome…>.app` | `/Applications/<Nome…>.app` |

- **Per tutto il computer** servono i permessi di amministratore: li chiede
  il sistema (UAC su Windows, `pkexec` su Linux) solo per il passo che sposta
  i file. Su macOS `/Applications` è scrivibile da un utente amministratore.
- Su Linux crea la voce nel menu delle applicazioni, su Windows il
  collegamento nel menu Start.
- **«Installa» fa scegliere**, quando c'è da scegliere: quale **versione**
  (l'elenco delle release pubblicate, con l'ultima già selezionata), quale
  **pacchetto** se la release ne ha più d'uno adatto al computer, e dove
  metterlo. Se c'è una sola release con un solo pacchetto e il posto è già
  stato scelto, parte senza chiedere. Un programma installato in una versione
  che non è l'ultima resta lì: «Aggiorna tutti» non lo tocca, e il suo
  pulsante «Aggiorna…» riapre la scelta.
- Il pacchetto si riconosce dal nome (`*-Linux-x86_64.AppImage`,
  `*-Windows.zip`, `*-AppleSilicon.zip`, `*-Intel.zip`); nel repository si può
  anche indicare con un modello. Su Windows CentralHub sa comportarsi in tre
  modi, e il modo scelto lo ricorda per gli aggiornamenti:

  | Pacchetto | Cosa fa CentralHub |
  |---|---|
  | zip portable | lo estrae nella cartella dei programmi, crea il collegamento, lo aggiorna e lo toglie da sé |
  | un solo `.exe` che è il programma | lo copia nella cartella dei programmi con il nome del programma e crea il collegamento |
  | installatore (`setup.exe`, `.msi`) | lo avvia e lo lascia fare; poi legge fra le app installate di Windows la voce che ha lasciato, per sapere se il programma c'è ancora e come disinstallarlo |

  Un `.exe` dal cui nome non si capisce se è il programma o il suo
  installatore viene proposto in tutti e due i modi.

  Su Linux i modi sono due:

  | Pacchetto | Cosa fa CentralHub |
  |---|---|
  | AppImage | la mette in `~/App` (o in `/opt/<Nome>/`), crea la voce nel menu con la sua icona, la aggiorna e la toglie da sé |
  | `.deb` | lo installa con `apt` (il sistema chiede la password di amministratore; le dipendenze le risolve apt), ricorda il nome del pacchetto per sapere se c'è ancora, lo aggiorna e lo toglie con `apt remove`. Dove finiscono i file e la voce di menu lo decide il pacchetto |

  Quando una release ha tutti e due, l'AppImage è quella consigliata.

  Su macOS i modi sono tre:

  | Pacchetto | Cosa fa CentralHub |
  |---|---|
  | zip con l'app | la estrae con `ditto` (permessi, collegamenti e firma restano com'erano) in `~/Applications` o in `/Applications`, la aggiorna e la toglie da sé |
  | `.dmg` | monta l'immagine (sola lettura, senza finestra nel Finder), copia l'app che contiene con `ditto`, smonta; da lì è un'app come quella dello zip, e si aggiorna con l'uno o con l'altro. Un `.dmg` che contiene un `.pkg` invece di un'app non lo sa seguire e lo dice |
  | `.pkg` | lo apre l'Installer del sistema, che mostra la sua procedura e chiede la password. CentralHub legge dal pacchetto cosa lascerà fra le ricevute del sistema (`pkgutil`), le confronta prima e dopo (se l'Installer viene chiuso senza installare, non annota niente), le ricorda, e per disinstallare toglie i file elencati nelle ricevute (con la password di amministratore) e le fa dimenticare. Si rifiuta di toccare file fuori da `/Applications`, `/Library`, `/usr/local`, `/opt`, `/Users` |

  Quando una release ha più modi, lo zip è quello consigliato; senza zip, il
  `.dmg`.
- **Lo scambio è una rinomina**: o c'è la versione vecchia intera, o quella
  nuova intera. Riuscito lo scambio **la vecchia viene cancellata**.
- **Un programma aperto non viene chiuso**: CentralHub lo dice e aspetta.
- Un programma con lo stesso nome **non installato da CentralHub** viene
  rilevato (su macOS con la sua versione) e sostituito solo dopo una conferma.
- **«Disinstalla»** toglie il programma dal computer: i suoi file, la voce di
  menu o il collegamento e ciò che CentralHub aveva annotato; un programma
  messo dal suo installatore lo toglie il suo disinstallatore, che CentralHub
  avvia. Prima mostra le cartelle di **impostazioni e dati** del programma
  che ha trovato e chiede se tenerle: se si sceglie di toglierle vanno nel
  **cestino** (su macOS quello vero, con `NSFileManager`: una rinomina in `~/.Trash` non è permessa a un'app senza «Accesso completo al disco»), altrimenti restano dove sono. Il repository resta nella lista.
- CentralHub può **aggiornare sé stesso**: poi va chiuso e riaperto (la
  versione vecchia la toglie al riavvio). Non può disinstallare sé stesso.

### In comune

- **Trova da solo l'ultima release** a ogni controllo: nessun link da
  aggiornare a mano. Bozze e prerelease non contano.
- **Due LED per repository**: l'esito dell'ultimo lavoro, e *aggiornato*
  (verde) / *da aggiornare* (rosso), calcolato all'apertura.
- **Un pulsante per tutti**, oppure un repository alla volta.
- **Tag** per raggruppare i repository e filtrarli.
- Togliere un repository dalla lista non cancella niente dal disco.

## Repository pubblici e privati

Nel programma non c'è nessun repository: la lista la fa chi lo usa, con
**Aggiungi repository** (o importando un file JSON). Un repository **pubblico**
si aggiunge e basta. Per uno **privato** si spunta «Repository privato» e si
incolla la sua **chiave di accesso**.

La chiave è un *fine-grained personal access token* di GitHub, **in sola
lettura**: conviene farne **una per repository**, limitata a quel solo
repository e con il solo permesso **Contents: Read-only**. Così revocarne una
su GitHub spegne un solo repository, e CentralHub non può scrivere da nessuna
parte.

- La chiave sta in `config.json`, nella cartella di configurazione di questo
  computer, **in chiaro** ma in un file leggibile solo dall'utente (permessi
  `0600`). Non è mai mostrata nell'interfaccia né scritta nel registro.
- Va solo ad `api.github.com`: quando GitHub rimanda a un altro indirizzo per
  il file vero, la chiave non lo segue.
- Un repository privato **senza chiave** resta nella lista, ma GitHub non lo
  mostra: CentralHub non può controllare, archiviare né installare finché la
  chiave non c'è («Aggiungi la chiave…»). Lo prova comunque senza chiave a ogni
  controllo: se il repository è **diventato pubblico**, funziona e la spunta
  «privato» si toglie da sola.

### Esportare e importare

**Esporta** salva in un JSON i repository, i tag e le impostazioni, e chiede
se includere anche le chiavi. Il nome proposto per il file porta la data e dice
se le chiavi ci sono (`CentralHub-configurazione-2026-10-15-con-chiavi.json`,
`…-senza-chiavi.json`):

- **Con le chiavi**: il file è un backup completo. Chiunque lo legga può usare
  le chiavi: va tenuto come una password.
- **Senza le chiavi**: il file ha tutto il resto, e per ogni repository
  privato l'indicazione che serve una chiave.

**Importa** *aggiunge* alla lista i repository del file, e completa quelli già
presenti con le sue impostazioni; non toglie niente, e la chiave che c'è già
resta se il file non ne porta una. I repository privati importati senza chiave
compaiono subito, con l'avviso che manca; la si aggiunge da «Modifica». I
percorsi di cartella di un altro sistema (`/Volumes/…` su Windows, per
esempio) non valgono e vengono lasciati vuoti. I file della 0.1.0 si
importano senza perdere niente.

Per i repository **pubblici** GitHub concede 60 richieste all'ora a tutto il
computer. CentralHub ne usa una sola per repository, per sapere qual è
l'ultima release; i file li prende dal loro indirizzo pubblico
(`github.com/…/releases/download/…`), che non consuma quel limite.

## Dove salva i dati

| Cosa | Dove |
|---|---|
| Repository, loro chiavi, tag, cartelle d'archivio, dove installare | `config.json` nella cartella di configurazione del sistema (`~/Library/Application Support/CentralHub`, `%APPDATA%\CentralHub`, `~/.config/CentralHub`); a ogni salvataggio resta la copia `config.json.bak` |
| Esito dell'ultimo lavoro | `stato.json`, nella stessa cartella |
| Cosa ha installato, e in quale versione | `installati/<Nome>.json`, nella stessa cartella |

Il file sta fuori dalla cartella del programma: **aggiornare CentralHub non
lo tocca**. Si **esporta e si importa** dall'interfaccia (backup, passaggio a
un altro computer); le chiavi ci finiscono solo se lo si chiede.

La variabile `CENTRALHUB_CONFIG_DIR` sposta la cartella di configurazione e
mette CentralHub in **modalità di prova**: i programmi si installano in due
cartelle dentro quella di prova, non in quelle vere del computer (salvo
`CENTRALHUB_CARTELLE_VERE=1`), e con `CENTRALHUB_API` si può puntare a un
GitHub di prova locale (`go run ./cmd/githubfinto`).

## Compilare dai sorgenti

Servono [Go](https://go.dev) e la riga di comando di Wails:

```bash
go install github.com/wailsapp/wails/v2/cmd/wails@v2.13.0
```

Su Linux anche `libgtk-3-dev`, `libwebkit2gtk-4.1-dev`, `build-essential` e
`pkg-config`. Poi, per il sistema su cui si è:

```bash
bash scripts/build.sh
```

```bash
bash scripts/pacchetto.sh
```

L'interfaccia (`frontend/ui`) è HTML, CSS e JavaScript senza passo di build.
La prova completa del proprio sistema, la stessa che precede ogni release:

```bash
bash scripts/ci-locale.sh
```

## Struttura del progetto

```
main.go, app.go            finestra Wails e comandi per l'interfaccia
internal/config/           repository, chiavi, tag, impostazioni, importa/esporta
internal/github/           ultima release e download dall'API di GitHub
internal/scarica/          download atomico di una release, o di un pacchetto
internal/installa/         installa e aggiorna il programma, per l'utente o per tutti
cmd/githubfinto/           un GitHub di prova locale
frontend/ui/               interfaccia
scripts/                   build, pacchetto, verifica, prova locale, release
```

## Licenza

[MIT](LICENSE) © 2026 Trismegisto Open Source. Il software è libero: si può
usare, copiare, modificare e ridistribuire, anche in programmi commerciali,
purché resti l'avviso di copyright. I componenti di terze parti e le loro
licenze (tutte permissive) sono in
[THIRD-PARTY-LICENSES.md](THIRD-PARTY-LICENSES.md); i testi viaggiano con
l'app, nella cartella `LICENZE`.

Contributi e segnalazioni: [CONTRIBUTING.md](CONTRIBUTING.md). Problemi di
sicurezza: [SECURITY.md](SECURITY.md).
