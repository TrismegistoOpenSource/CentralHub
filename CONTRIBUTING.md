# Contribuire a CentralHub

Grazie dell'interesse. CentralHub è software libero con licenza [MIT](LICENSE):
inviando un contributo accetti che venga distribuito con la stessa licenza.

## Segnalare un problema

Apri una *issue* dicendo: sistema operativo e versione di CentralHub (in alto
nella finestra), cosa hai fatto, cosa ti aspettavi e cosa è successo. Il
**Registro** dell'app (pulsante in alto a destra) aiuta; **non incollare mai
una chiave di accesso**.

## Proporre una modifica

1. Fai un *fork* e lavora su un ramo a parte.
2. Compila e prova sul tuo sistema:

   ```bash
   bash scripts/ci-locale.sh
   ```

   Esegue build, `go vet`, `gofmt`, i test (con `-race`), il pacchetto e la sua
   verifica. Servono Go e Wails (vedi il [README](README.md)).
3. Le prove non devono mai toccare la configurazione vera dell'utente: usano
   una cartella temporanea (`CENTRALHUB_CONFIG_DIR`, `t.TempDir()` nei test).
4. Ogni file `.go` comincia con `// SPDX-License-Identifier: MIT`.
5. Apri una *pull request* spiegando il perché della modifica.

## Dipendenze

Il binario incorpora Wails e i moduli che si porta dietro. Una nuova
dipendenza deve avere una licenza permissiva (MIT, BSD, ISC, Apache-2.0) e
comparire in [THIRD-PARTY-LICENSES.md](THIRD-PARTY-LICENSES.md).
