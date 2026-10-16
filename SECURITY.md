# Sicurezza

## Segnalare una vulnerabilità

Non aprire una *issue* pubblica: usa la segnalazione privata di GitHub
(scheda **Security** del repository → *Report a vulnerability*).

## Cosa sapere sulle chiavi di accesso

CentralHub può usare una chiave (token GitHub) per i repository privati.

- Ne conviene una per repository, **fine-grained**, con il solo permesso
  *Contents: Read-only*: CentralHub non scrive mai su GitHub.
- La chiave sta **in chiaro** nel `config.json` del computer, in un file
  leggibile solo dall'utente (`0600`). Chi ha accesso a quel file, o a un file
  esportato *con le chiavi*, può usarle: va trattato come una password.
- La chiave viene mandata soltanto ad `api.github.com`, mai ai server a cui
  GitHub rimanda per scaricare i file, e non compare nell'interfaccia né nel
  registro.
- Se una chiave è stata esposta, si revoca su GitHub (*Settings → Developer
  settings → Personal access tokens*) e se ne crea un'altra.
