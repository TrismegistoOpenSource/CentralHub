# Autoverifica all'avvio

All'avvio, a finestra già aperta e senza ritardare niente, CentralHub controlla da sé di essere integro e, quando la release è pubblica, di
essere stato pubblicato da Trismegisto Open Source. L'esito compare nella barra di stato in basso (passando sopra con il puntatore si legge il dettaglio)
ed è scritto in `<cartella di configurazione>/autoverifica-stato.json`. Non blocca mai l'uso. Una volta per versione scarica tre file pubblici della release.

Il codice è `internal/autoverifica` (solo libreria standard, identico in tutti i programmi di Trismegisto Open Source) e
`internal/firma` (la chiave pubblica incorporata, copiata identica da CentralHub).

## Che cosa significano le righe

| Riga | Significato |
|---|---|
| **Integro e firmato da Trismegisto** | I file coincidono con l'elenco della release e l'elenco è coperto dalla firma. |
| **Integro, firma non confermata (…)** | I file coincidono con l'elenco del pacchetto; la firma non si è potuta confermare: *repository privato*, *nessuna rete* o *release non firmata*. |
| **File diversi dall'originale** | Un file è cambiato o manca (anche un antivirus che lo mutila): riscarica il programma. |
| **Firma della release non valida** | La firma non corrisponde alla chiave incorporata, o l'elenco non è quello firmato. |
| **Non verificabile** | Versione di sviluppo o pacchetto non confezionato dal rilascio: non c'è l'elenco `INTEGRITA.txt`. |

## La catena

```
chiave pubblica nel programma (internal/firma/chiavi.go)
   └─ verifica SHA256SUMS.txt.sig  →  SHA256SUMS.txt (firmato)
        └─ elenca INTEGRITA-<sistema>.txt  (con il suo SHA-256)
             └─ elenca lo SHA-256 di ogni file del pacchetto, eseguibile compreso
```

La firma la mette chi pubblica, dopo il run verde, con `bash scripts/firma-release.sh <proprietario/repo> <tag>` (chiave privata in
`~/.centralhub-firma`, mai nel workspace né su GitHub). Gli elenchi li scrive `scripts/integrita-pacchetto.sh` durante `build.sh` e
`pacchetto.sh`; `verifica-pacchetto.sh` controlla che il pacchetto li rispetti **in entrambe le direzioni** (un file alterato a mano
deve far fallire il cancello) e, con `--avvio`, che l'app scriva davvero un esito «integro».

- **Dentro il pacchetto**: `INTEGRITA.txt` (Linux: radice della AppDir; Windows: accanto all'eseguibile; macOS: `Contents/Resources/`).
  Su macOS non contiene l'eseguibile né `_CodeSignature`, perché la firma del sistema li cambia dopo: li copre l'elenco della release.
- **Fra gli asset della release**: `INTEGRITA-macos-arm64.txt`, `INTEGRITA-macos-amd64.txt`, `INTEGRITA-linux-amd64.txt`,
  `INTEGRITA-windows-amd64.txt`, quindi elencati nel `SHA256SUMS.txt` firmato.

## Rete

Il repository `TrismegistoOpenSource/CentralHub` è pubblico: all'avvio l'app scarica tre file pubblici della release (`SHA256SUMS.txt`, la firma, l'elenco del
sistema), **una volta per versione** (poi basta la cache `autoverifica-cache.json`). Nessun dato dell'utente parte. Una release
appena pubblicata e non ancora firmata si legge come «firma non confermata (release non firmata)» finché chi pubblica non lancia
`scripts/firma-release.sh`.

## Cosa può dare e cosa no

Un programma che controlla sé stesso può essere aggirato da chi lo ricompila (toglie il controllo o cambia la chiave). Protegge da
download corrotti o troncati, antivirus che mutila un file, risorse sostituite, pacchetti ricaricati altrove con file cambiati; fa
vedere «firmato da Trismegisto» oppure «non confermato». **Non** protegge da un eseguibile sostituito con uno che non controlla
niente: per quello restano la firma verificata prima di installare (CentralHub) e quella del sistema operativo.

## Cambiare la chiave

Si **aggiunge** la nuova in `ChiaviPubbliche` (`internal/firma/chiavi.go`), si pubblica una versione, e solo dopo si toglie la vecchia.
Il file deve restare uguale a quello di CentralHub.
