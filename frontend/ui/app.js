// SPDX-License-Identifier: MIT

"use strict";

// Interfaccia di CentralHub. Nessuna logica qui: mostra ciò che dice il lato
// Go (window.go.main.App) e gli passa i comandi dell'utente.
const api = () => window.go.main.App;
const $ = (id) => document.getElementById(id);

// Il sistema, subito: lo spazio a sinistra della barra serve ai semafori
// della finestra di macOS (TitleBarHiddenInset).
const piattaforma = navigator.platform.toLowerCase().includes("mac") ? "darwin"
  : navigator.platform.toLowerCase().includes("win") ? "windows" : "linux";
document.documentElement.dataset.os = piattaforma;

const stato = {
  repo: new Map(), // id -> vista
  ordine: [],
  panoramica: null,
  modo: localStorage.getItem("modo") === "archivio" ? "archivio" : "update",
  tag: null,        // filtro: null = tutti
  inModifica: null, // id del repository nella finestra, null = nuovo
  ambito: "",
};

function esc(s) {
  return String(s ?? "").replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
}

function messaggio(e) {
  return (e && e.message) ? e.message : String(e);
}

function barra(testo, errore = false) {
  const b = $("banner");
  if (!testo) { b.hidden = true; return; }
  b.textContent = testo;
  b.classList.toggle("error", errore);
  b.hidden = false;
  clearTimeout(barra.t);
  if (!errore) barra.t = setTimeout(() => { b.hidden = true; }, 6000);
}

function dimensione(n) {
  if (n >= 1 << 30) return (n / (1 << 30)).toFixed(2) + " GB";
  if (n >= 1 << 20) return (n / (1 << 20)).toFixed(1) + " MB";
  return Math.max(1, Math.round(n / 1024)) + " kB";
}

function quando(iso) {
  const d = new Date(iso);
  if (isNaN(d)) return "";
  return d.toLocaleDateString("it-IT", { day: "2-digit", month: "2-digit" }) + " " +
    d.toLocaleTimeString("it-IT", { hour: "2-digit", minute: "2-digit" });
}

const AMBITI = { utente: "solo per me", sistema: "tutto il computer" };

// locale raccoglie da una vista ciò che riguarda questo computer.
function locale(v, cambi = {}) {
  return Object.assign({ tag: v.tag, cartellaArchivio: v.cartellaArchivio, aggiorna: v.aggiorna, ambito: v.ambito }, cambi);
}

// ---- registro ----------------------------------------------------------------

function riga(testo) {
  const corpo = $("log-body");
  corpo.append(document.createTextNode(testo + "\n"));
  // il registro non cresce senza limite
  while (corpo.childNodes.length > 1500) corpo.firstChild.remove();
  corpo.scrollTop = corpo.scrollHeight;
}

// ---- le due modalità ---------------------------------------------------------

const FASI = {
  controllo: "Controllo l'ultima release…", download: "Download", installazione: "Installazione…",
  installatore: "Installatore aperto: va seguita la sua finestra…",
  disinstallazione: "Disinstallazione… (se si apre una finestra del programma, va seguita)",
};

// I generi di pacchetto: come si chiamano e cosa ne fa CentralHub.
const TIPI = {
  portable: ["Versione portable (zip)", "CentralHub la mette nella cartella dei programmi, crea il collegamento nel menu Start, la aggiorna e la toglie da sé."],
  eseguibile: ["Programma in un solo file (.exe)", "CentralHub lo copia nella cartella dei programmi e crea il collegamento nel menu Start."],
  installatore: ["Installatore (setup o .msi)", "CentralHub lo avvia e lo lascia fare: dove installare e cosa chiedere lo decide lui. Va seguita la sua finestra."],
  deb: ["Pacchetto .deb", "CentralHub lo installa con apt (il sistema chiede la password di amministratore), lo aggiorna e lo toglie da sé. Le dipendenze le risolve apt."],
  appimage: ["AppImage", "CentralHub la mette al suo posto, crea la voce nel menu, la aggiorna e la toglie da sé."],
  app: ["App per macOS (zip)", "CentralHub la mette in Applicazioni, la aggiorna e la toglie da sé."],
  dmg: ["Immagine disco (.dmg)", "CentralHub apre l'immagine, copia l'app in Applicazioni, la aggiorna e la toglie da sé."],
  pkg: ["Pacchetto .pkg", "Lo apre l'Installer di macOS, che chiede la password di amministratore. CentralHub lo riconosce poi dalle ricevute del sistema, lo aggiorna e lo toglie (anche questo con la password)."],
};

// seguito dice se un repository è fra quelli su cui lavora «… tutti» nella
// modalità aperta.
function seguito(v) {
  return stato.modo === "archivio" ? !!v.cartellaArchivio : (v.installato.presente || v.aggiorna);
}

function aggiornato(v) {
  return stato.modo === "archivio" ? v.archivioAggiornato : v.updateAggiornato;
}

// azione dice cosa fa il pulsante principale di un repository.
function azione(v) {
  if (v.manca) return { testo: "Aggiungi la chiave…", az: "modifica" };
  if (stato.modo === "archivio") {
    if (!v.cartellaArchivio) return { testo: "Scegli la cartella…", az: "cartella-archivio" };
    return v.archivioAggiornato === "si" ? { testo: "Archiviata", fatto: true } : { testo: "Archivia", az: "archivia" };
  }
  if (v.installabile === "no") return { testo: "Niente per questo sistema", fatto: true };
  if (!v.installato.presente) return { testo: "Installa", az: "aggiorna" };
  if (v.updateAggiornato === "si") return { testo: "Aggiornato", fatto: true };
  // i puntini: prima di partire c'è qualcosa da scegliere o da confermare
  return daChiedere(v) || !(v.installato.nostro || v.installato.versione) ? { testo: "Aggiorna…", az: "aggiorna" } : { testo: "Aggiorna", az: "aggiorna" };
}

// daChiedere dice se prima di installare o aggiornare va aperta la finestra
// delle scelte: alla prima installazione (quale versione, quale pacchetto,
// dove), per un programma fermo su una versione scelta dall'utente, e quando
// la release ha più pacchetti adatti fra cui CentralHub non sa decidere.
function daChiedere(v) {
  return !v.installato.presente || v.installato.fissata || v.scelta;
}

function disegnaRepo(v) {
  const archivio = stato.modo === "archivio";
  const lavora = !!v.fase;
  const esitoModo = archivio ? v.esitoArchivio : v.esitoUpdate;

  // primo LED: esito dell'ultimo lavoro in questa modalità
  let led1 = "", t1 = "Mai eseguito";
  if (lavora) { led1 = "running"; t1 = "In corso"; }
  else if (esitoModo) { led1 = esitoModo.ok ? "success" : "failed"; t1 = esitoModo.ok ? "Ultimo lavoro riuscito" : "Ultimo lavoro non riuscito"; }
  // secondo LED: aggiornato / da aggiornare
  const agg = aggiornato(v);
  let led2 = "", t2 = seguito(v) ? "Non controllato" : (archivio ? "Non archiviato" : "Non installato");
  if (agg === "si") { led2 = "success"; t2 = "Aggiornato"; }
  else if (agg === "no") { led2 = "failed"; t2 = "Da aggiornare"; }

  const versioni = [`<span class="${agg === "no" ? "nuova" : ""}">Ultima release <b>${esc(v.ultimaRelease || "…")}</b></span>`];
  if (archivio) {
    versioni.push(`<span>Archiviata <b>${esc(v.cartellaArchivio ? (v.archiviata || "nessuna") : "—")}</b></span>`);
  } else {
    let inst = "no";
    if (v.installato.presente) {
      inst = v.installato.nostro ? v.installato.tag : (v.installato.versione || "sì, versione sconosciuta");
      inst += " · " + (v.installato.tipo === "installatore" ? "dal suo installatore" : v.installato.tipo === "deb" ? "pacchetto .deb (apt)" : v.installato.tipo === "pkg" ? "pacchetto .pkg" : AMBITI[v.installato.ambito]);
      if (v.installato.fissata) inst += " · versione scelta da te";
    } else if (v.ambito) {
      inst += " · andrà: " + AMBITI[v.ambito];
    }
    versioni.push(`<span>Installato <b>${esc(inst)}</b></span>`);
  }

  let esito = "", classe = "";
  if (lavora) {
    classe = "lavoro";
    esito = FASI[v.fase] || v.fase;
    if (v.fase === "download" && v.file) {
      esito += " · " + v.file + " · " + dimensione(v.fatti) + (v.totale > 0 ? " di " + dimensione(v.totale) : "");
    }
  } else if (v.manca) {
    classe = "errore"; esito = "Manca la chiave di accesso di questo repository privato: finché non c'è, non si può controllare, archiviare né installare.";
  } else if (v.errore) {
    classe = "errore"; esito = v.errore;
  } else if (esitoModo) {
    classe = esitoModo.ok ? "" : "errore";
    esito = quando(esitoModo.quando) + " · " + esitoModo.messaggio;
  } else if (!archivio && v.installabile === "no") {
    esito = "L'ultima release non ha un pacchetto installabile su questo sistema.";
  }

  let avanzamento = "";
  if (lavora) {
    const pct = v.fase === "download" && v.totale > 0 ? Math.min(100, (v.fatti / v.totale) * 100) : -1;
    avanzamento = pct < 0
      ? `<div class="progress indeterminate"><div class="progress-fill"></div></div>`
      : `<div class="progress"><div class="progress-fill" style="width:${pct.toFixed(1)}%"></div></div>`;
  }

  const a = azione(v);
  const principale = lavora
    ? `<button class="btn danger-ghost" data-az="annulla">Interrompi</button>`
    : `<button class="btn ${a.fatto ? "" : "primary"}" data-az="${a.az || ""}" ${a.fatto ? "disabled" : ""}>${esc(a.testo)}</button>`;

  const percorso = archivio ? v.cartellaArchivio : (v.installato.presente ? v.installato.percorso : "");
  const secondari = [];
  if (percorso) secondari.push(`<button class="btn ghost small" data-az="apri" title="${esc(percorso)}">Cartella</button>`);
  if (!archivio && !v.installato.presente && v.installabile !== "no" && !v.manca) {
    secondari.push(`<button class="btn ghost small" data-az="segui" ${lavora ? "disabled" : ""} title="I programmi seguiti vengono installati e aggiornati da «Aggiorna tutti»">${v.aggiorna ? "Non seguire" : "Segui"}</button>`);
  }
  if (!archivio && v.installato.presente) {
    secondari.push(`<button class="btn ghost small" data-az="disinstalla" ${lavora ? "disabled" : ""} title="Toglie il programma dal computer">Disinstalla</button>`);
  }
  secondari.push(`<button class="btn ghost small" data-az="modifica" ${lavora ? "disabled" : ""}>Modifica</button>`);
  secondari.push(`<button class="btn ghost small" data-az="rimuovi" ${lavora ? "disabled" : ""} title="Toglie il repository dalla lista, con la sua chiave: non cancella niente dal disco">Togli</button>`);

  const el = document.createElement("article");
  el.className = "card" + (seguito(v) || lavora ? "" : " spenta");
  el.dataset.id = v.id;
  el.innerHTML = `
    <div class="led-colonna">
      <span class="led-riga" title="${esc(t1)}"><span class="status-dot ${led1}"></span>${esc(t1)}</span>
      <span class="led-riga" title="${esc(t2)}"><span class="status-dot ${led2}"></span>${esc(t2)}</span>
    </div>
    <div class="card-body">
      <div class="card-title"><h3>${esc(v.nome)}</h3><span class="card-repo">${esc(v.repo)}</span>${v.privato ? `<span class="etichetta ${v.manca ? "manca" : ""}" title="${v.manca ? "Repository privato: manca la chiave di accesso" : "Repository privato, con la sua chiave di accesso"}">${v.manca ? "privato · manca la chiave" : "privato"}</span>` : ""}</div>
      <div class="card-versioni">${versioni.join("")}</div>
      ${percorso ? `<div class="card-path" title="${esc(percorso)}">&#x202A;${esc(percorso)}&#x202C;</div>` : ""}
      <div class="card-esito ${classe}">${esc(esito)}</div>
      ${avanzamento}
    </div>
    <div class="card-actions">
      ${principale}
      <div class="riga">${secondari.join("")}</div>
    </div>`;
  return el;
}

function visibili() {
  return stato.ordine.map((i) => stato.repo.get(i)).filter((v) => stato.tag === null || (v.tag || "") === stato.tag);
}

function disegnaLista() {
  const lista = $("lista");
  const viste = visibili();
  // gruppi per tag, in ordine alfabetico; in fondo quelli senza tag
  const gruppi = new Map();
  for (const v of viste) {
    const t = v.tag || "";
    if (!gruppi.has(t)) gruppi.set(t, []);
    gruppi.get(t).push(v);
  }
  const chiavi = [...gruppi.keys()].sort((a, b) => (a === "") - (b === "") || a.localeCompare(b, "it", { sensitivity: "base" }));
  const nodi = [];
  for (const k of chiavi) {
    if (chiavi.length > 1 || k !== "") {
      const h = document.createElement("div");
      h.className = "group-header";
      h.innerHTML = `${esc(k || "Senza tag")}<span class="conto">${gruppi.get(k).length}</span>`;
      nodi.push(h);
    }
    for (const v of gruppi.get(k)) nodi.push(disegnaRepo(v));
  }
  lista.replaceChildren(...nodi);
  $("vuoto").hidden = stato.ordine.length > 0;
  lista.hidden = stato.ordine.length === 0;
}

function disegnaTag() {
  const tutti = [...new Set([...stato.repo.values()].map((v) => v.tag || ""))].filter((t) => t !== "")
    .sort((a, b) => a.localeCompare(b, "it", { sensitivity: "base" }));
  if (stato.tag !== null && !tutti.includes(stato.tag)) stato.tag = null;
  const box = $("tag-chips");
  if (tutti.length === 0) { box.replaceChildren(); return; }
  const chip = (testo, valore) => {
    const b = document.createElement("button");
    b.className = "chip" + (stato.tag === valore ? " active" : "");
    b.textContent = testo;
    b.addEventListener("click", () => { stato.tag = valore; ridisegna(); });
    return b;
  };
  box.replaceChildren(chip("Tutti", null), ...tutti.map((t) => chip(t, t)));
  $("repo-tag-noti").replaceChildren(...tutti.map((t) => { const o = document.createElement("option"); o.value = t; return o; }));
}

function aggiornaBarre() {
  const archivio = stato.modo === "archivio";
  document.querySelectorAll("#modo button").forEach((b) => b.classList.toggle("active", b.dataset.modo === stato.modo));
  $("btn-tutti-testo").textContent = archivio ? "Archivia tutti" : "Aggiorna tutti";
  $("btn-tutti").title = archivio
    ? "Scarica l'ultima release di ogni repository che ha una cartella d'archivio"
    : "Installa e aggiorna i programmi già installati e quelli seguiti";
  $("modo-nota").innerHTML = archivio
    ? "<strong>Archiviazione</strong>: ogni release in una sottocartella della cartella scelta, con tutti i suoi pacchetti come file separati. Non installa niente."
    : "<strong>Update</strong>: installa e aggiorna i programmi su questo computer. Lavora su quelli già installati e su quelli che scegli di seguire.";

  const viste = [...stato.repo.values()];
  const lavorano = viste.filter((v) => v.fase).length;
  const seguiti = viste.filter(seguito);
  $("btn-tutti").disabled = seguiti.length === 0 || lavorano > 0;
  $("btn-controlla").disabled = viste.length === 0 || lavorano > 0;
  const daFare = seguiti.filter((v) => aggiornato(v) === "no").length;
  const t = $("stato-testo");
  t.classList.toggle("busy", lavorano > 0);
  if (lavorano > 0) t.textContent = lavorano === 1 ? "Un repository al lavoro…" : lavorano + " repository al lavoro…";
  else if (daFare > 0) t.textContent = (daFare === 1 ? "Uno da aggiornare" : daFare + " da aggiornare") + (archivio ? " in archivio" : "");
  else if (seguiti.length > 0 && seguiti.every((v) => aggiornato(v) === "si")) t.textContent = "Tutto aggiornato";
  else t.textContent = "Pronto";
}

function ridisegna(id) {
  if (id) {
    const v = stato.repo.get(id);
    const vecchio = $("lista").querySelector(`[data-id="${CSS.escape(id)}"]`);
    // la sola scheda, se il repository resta dov'è (stesso tag, stesso filtro)
    if (v && vecchio && vecchio.dataset.tag === (v.tag || "")) {
      const nuovo = disegnaRepo(v);
      nuovo.dataset.tag = v.tag || "";
      vecchio.replaceWith(nuovo);
      aggiornaBarre();
      return;
    }
  }
  disegnaTag();
  disegnaLista();
  $("lista").querySelectorAll(".card").forEach((c) => { c.dataset.tag = stato.repo.get(c.dataset.id).tag || ""; });
  aggiornaBarre();
}

function metti(v) {
  if (!stato.repo.has(v.id)) stato.ordine.push(v.id);
  stato.repo.set(v.id, v);
}

async function ricarica() {
  const p = await api().Stato();
  stato.panoramica = p;
  stato.repo.clear();
  stato.ordine = [];
  p.repo.forEach(metti);
  $("versione").textContent = p.versione;
  $("file-config").textContent = (p.prova ? "modalità di prova · " : "") + p.fileConfig;
  $("file-config").title = "File della configurazione: " + p.fileConfig;
  for (const id of ["amb-utente", "amb2-utente", "amb3-utente"]) $(id).textContent = p.cartelle.utente;
  for (const id of ["amb-sistema", "amb2-sistema", "amb3-sistema"]) $(id).textContent = p.cartelle.sistema + " (permessi di amministratore)";
  ridisegna();
}

// ---- conferma e scelta dell'ambito -------------------------------------------

function conferma(titolo, corpoHtml, ok) {
  return new Promise((risolvi) => {
    $("conf-titolo").textContent = titolo;
    $("conf-corpo").innerHTML = corpoHtml;
    $("conf-ok").textContent = ok;
    $("dlg-conferma").hidden = false;
    const chiudi = (esito) => {
      $("dlg-conferma").hidden = true;
      $("conf-ok").onclick = $("conf-annulla").onclick = null;
      risolvi(esito);
    };
    $("conf-ok").onclick = () => chiudi(true);
    $("conf-annulla").onclick = () => chiudi(false);
  });
}

// chiediAmbito chiede dove installare un programma: "utente", "sistema",
// oppure "" se l'utente rinuncia.
function chiediAmbito(v) {
  return new Promise((risolvi) => {
    $("ambito-titolo").textContent = "Dove installo " + v.nome + "?";
    $("dlg-ambito").hidden = false;
    const chiudi = (esito) => {
      $("dlg-ambito").hidden = true;
      $("ambito-scelta").onclick = $("ambito-annulla").onclick = null;
      risolvi(esito);
    };
    $("ambito-scelta").onclick = (ev) => {
      const b = ev.target.closest("button[data-ambito]");
      if (b) chiudi(b.dataset.ambito);
    };
    $("ambito-annulla").onclick = () => chiudi("");
  });
}

// ---- azioni sui repository ---------------------------------------------------

async function imposta(v, cambi) {
  const nuova = await api().ImpostaLocale(v.id, locale(v, cambi));
  metti(nuova);
  ridisegna();
  return nuova;
}

// chiediInstallazione fa scegliere quale versione installare, quale
// pacchetto e (alla prima installazione) dove. Restituisce
// { opzioni: { tag, asset, tipo }, ambito } oppure null se l'utente rinuncia.
// Se non c'è niente da scegliere non apre nessuna finestra.
function chiediInstallazione(v, scelte) {
  const installato = v.installato.presente;
  // un programma già installato resta del suo genere: per cambiarlo va prima disinstallato
  // un'app di macOS arriva sia da uno zip sia da un .dmg: si aggiorna con l'uno o con l'altro
  const genere = (t) => (t === "app" || t === "dmg" ? "app" : t);
  const adatti = (r) => {
    if (!installato) return r.pacchetti;
    const esatti = r.pacchetti.filter((p) => p.tipo === v.installato.tipo);
    return esatti.length > 0 ? esatti : r.pacchetti.filter((p) => genere(p.tipo) === genere(v.installato.tipo));
  };
  const release = scelte.release.filter((r) => adatti(r).length > 0);
  if (release.length === 0) {
    return Promise.reject(new Error("Nessuna release ha un pacchetto dello stesso genere di quello installato: per cambiare genere il programma va prima disinstallato."));
  }
  let r = release.find((x) => x.ultima) || release[0];
  const predefinito = (x) => { const p = adatti(x); return p.length === 1 ? p[0] : (p.find((y) => y.consigliato) || null); };
  let pacchetto = predefinito(r);
  let ambito = v.ambito || "";
  const serveAmbito = () => !installato && pacchetto && pacchetto.tipo !== "installatore" && pacchetto.tipo !== "deb" && pacchetto.tipo !== "pkg";
  if (release.length === 1 && adatti(r).length === 1 && !(serveAmbito() && !ambito)) {
    return Promise.resolve({ opzioni: { tag: r.tag, asset: pacchetto.nome, tipo: pacchetto.tipo }, ambito });
  }

  return new Promise((risolvi) => {
    $("inst-titolo").textContent = (installato ? "Aggiorna " : "Installa ") + v.nome;
    $("inst-ok").textContent = installato ? "Aggiorna" : "Installa";
    $("inst-avviso").textContent = scelte.avviso || "";
    $("inst-avviso").hidden = !scelte.avviso;
    $("inst-versioni-nota").textContent = release.length === 1 ? "(c'è solo questa)" : "(" + release.length + " release, dalla più recente)";
    const sel = $("inst-versione");
    sel.replaceChildren(...release.map((x) => {
      const o = document.createElement("option");
      o.value = x.tag;
      const data = x.pubblicata ? new Date(x.pubblicata) : null;
      o.textContent = x.tag + (x.ultima ? " · ultima" : "") + (x.prerelease ? " · prerelease" : "") +
        (installato && v.installato.tag === x.tag ? " · installata" : "") +
        (data && !isNaN(data) ? " · " + data.toLocaleDateString("it-IT") : "");
      return o;
    }));
    sel.value = r.tag;
    sel.disabled = release.length === 1;

    const disegna = () => {
      const p = adatti(r);
      $("inst-pacchetti-nota").textContent = p.length === 1 ? "(questa versione ne ha uno solo per questo computer)" : "(scegli quale)";
      $("inst-pacchetti").replaceChildren(...p.map((x) => {
        const b = document.createElement("button");
        const t = TIPI[x.tipo] || [x.tipo, ""];
        b.className = pacchetto && pacchetto.nome === x.nome && pacchetto.tipo === x.tipo ? "active" : "";
        b.innerHTML = `<strong>${esc(t[0])}</strong><span>${esc(x.nome)} · ${esc(dimensione(x.dimensione))}</span><em>${esc(t[1])}</em>`;
        b.addEventListener("click", () => { pacchetto = x; disegna(); });
        return b;
      }));
      $("inst-campo-ambito").hidden = !serveAmbito();
      document.querySelectorAll("#inst-ambito button").forEach((b) => b.classList.toggle("active", b.dataset.ambito === ambito));
      $("inst-ok").disabled = !pacchetto || (serveAmbito() && !ambito);
    };
    const chiudi = (esito) => {
      $("dlg-installa").hidden = true;
      sel.onchange = $("inst-ambito").onclick = $("inst-ok").onclick = $("inst-annulla").onclick = null;
      risolvi(esito);
    };
    sel.onchange = () => { r = release.find((x) => x.tag === sel.value); pacchetto = predefinito(r); disegna(); };
    $("inst-ambito").onclick = (ev) => {
      const b = ev.target.closest("button[data-ambito]");
      if (b) { ambito = b.dataset.ambito; disegna(); }
    };
    $("inst-ok").onclick = () => chiudi({ opzioni: { tag: r.tag, asset: pacchetto.nome, tipo: pacchetto.tipo }, ambito: serveAmbito() ? ambito : v.ambito });
    $("inst-annulla").onclick = () => chiudi(null);
    disegna();
    $("dlg-installa").hidden = false;
  });
}

// aggiorna installa un programma o lo aggiorna. Restituisce vero se il
// lavoro è partito.
async function aggiorna(v) {
  const opzioni = { tag: "", asset: "", tipo: "", sostituisci: false };
  try {
    if (daChiedere(v)) {
      const scelte = await api().Scelte(v.id);
      v = stato.repo.get(v.id) || v;
      const scelta = await chiediInstallazione(v, scelte);
      if (!scelta) return false;
      if (scelta.ambito && scelta.ambito !== v.ambito) v = await imposta(v, { ambito: scelta.ambito });
      Object.assign(opzioni, scelta.opzioni);
    }
    if (v.installato.presente && !v.installato.nostro) {
      opzioni.sostituisci = await conferma("Sostituire il programma?",
        `Sul computer c'è già <strong>${esc(v.nome)}</strong>, ma non l'ha installato CentralHub` +
        (v.installato.versione ? ` (versione ${esc(v.installato.versione)}).` : " e non se ne conosce la versione.") +
        `<span class="path">${esc(v.installato.percorso)}</span>` +
        `Verrà sostituito con la release scelta, nello stesso posto: la copia attuale viene <strong>cancellata</strong>.`,
        "Sostituisci");
      if (!opzioni.sostituisci) return false;
    }
    await api().Aggiorna(v.id, opzioni);
    return true;
  } catch (e) { barra(messaggio(e), true); return false; }
}

// disinstalla toglie il programma dal computer, dopo aver detto cosa
// verrà tolto e aver chiesto se tenere le sue impostazioni.
async function disinstalla(v) {
  try {
    const info = await api().InfoRimozione(v.id);
    const i = info.installato;
    let corpo = `<strong>${esc(v.nome)}</strong> viene tolto dal computer`;
    if (i.tipo === "installatore") {
      corpo += `: lo fa il suo disinstallatore, che CentralHub avvia. Se apre una finestra, va seguita.`;
    } else if (i.tipo === "deb") {
      corpo += `, con apt: il sistema chiederà i permessi di amministratore.`;
    } else if (i.tipo === "pkg") {
      corpo += `: CentralHub toglie i file che il pacchetto ha installato (il sistema chiederà la password di amministratore) e poi lo fa dimenticare al sistema.<span class="path">${esc(i.percorso || "")}</span>`;
    } else {
      corpo += `, con la sua voce nel menu dei programmi.<span class="path">${esc(i.percorso)}</span>`;
      if (i.ambito === "sistema" && stato.panoramica.sistema !== "darwin") corpo += `È installato per tutto il computer: il sistema chiederà i permessi di amministratore. `;
    }
    if (!i.nostro) corpo += ` Non l'ha installato CentralHub.`;
    // dopo il riquadro del percorso basta un a capo; dopo una frase ne servono due
    const stacco = corpo.endsWith("</span>") ? "<br>" : "<br><br>";
    if (info.dati.length > 0) {
      corpo += stacco + `Sul computer ci sono anche le sue impostazioni e i suoi dati:` +
        info.dati.map((d) => `<span class="path">${esc(d)}</span>`).join("") +
        `<label class="switch-row"><span>Sposta nel cestino anche impostazioni e dati</span><input type="checkbox" id="dis-dati" /><i></i></label>` +
        `Se non lo attivi restano dove sono, e reinstallando il programma li ritrovi.`;
    } else {
      corpo += stacco + `Non ho trovato impostazioni o dati di questo programma da togliere.`;
    }
    corpo += `<br><br>Il repository resta nella lista di CentralHub.`;
    if (!await conferma("Disinstallare " + v.nome + "?", corpo, "Disinstalla")) return;
    const dati = $("dis-dati");
    await api().Disinstalla(v.id, !!(dati && dati.checked));
  } catch (e) { barra(messaggio(e), true); }
}

async function scegliCartellaArchivio(v) {
  try {
    const d = await api().ScegliCartella("Cartella d'archivio di " + v.nome, v.cartellaArchivio);
    if (d) await imposta(v, { cartellaArchivio: d });
  } catch (e) { barra(messaggio(e), true); }
}

async function rimuovi(v) {
  const ok = await conferma("Togliere il repository?",
    `<strong>${esc(v.nome)}</strong> viene tolto dalla lista di CentralHub. Dal disco non si cancella niente: ` +
    `le release archiviate e il programma installato restano dove sono.`,
    "Togli dalla lista");
  if (!ok) return;
  try {
    await api().RimuoviRepo(v.id);
    stato.repo.delete(v.id);
    stato.ordine = stato.ordine.filter((i) => i !== v.id);
    ridisegna();
  } catch (e) { barra(messaggio(e), true); }
}

$("lista").addEventListener("click", (ev) => {
  const b = ev.target.closest("button[data-az]");
  if (!b) return;
  const v = stato.repo.get(b.closest(".card").dataset.id);
  if (!v) return;
  const errore = (e) => barra(messaggio(e), true);
  switch (b.dataset.az) {
    case "aggiorna": aggiorna(v); break;
    case "archivia": api().Archivia(v.id).catch(errore); break;
    case "cartella-archivio": scegliCartellaArchivio(v); break;
    case "annulla": api().Annulla(v.id); break;
    case "apri": api().ApriCartella(stato.modo === "archivio" ? v.cartellaArchivio : cartellaDi(v.installato.percorso)).catch(errore); break;
    case "segui": imposta(v, { aggiorna: !v.aggiorna }).catch(errore); break;
    case "disinstalla": disinstalla(v); break;
    case "modifica": apriRepo(v); break;
    case "rimuovi": rimuovi(v); break;
  }
});

// cartellaDi restituisce la cartella che contiene un percorso.
function cartellaDi(p) {
  const i = Math.max(p.lastIndexOf("/"), p.lastIndexOf("\\"));
  return i > 0 ? p.slice(0, i) : p;
}

$("modo").addEventListener("click", (ev) => {
  const b = ev.target.closest("button[data-modo]");
  if (!b) return;
  stato.modo = b.dataset.modo;
  localStorage.setItem("modo", stato.modo);
  ridisegna();
});
// aggiornaTutti: prima di partire fa le domande che servono per i programmi
// seguiti e non ancora installati: dove installarli, se non è mai stato
// scelto, e quale pacchetto, se la release ne ha più d'uno adatto. Quelli
// partono subito, uno per uno; se per uno si rinuncia, quel programma si
// salta e gli altri proseguono.
async function aggiornaTutti() {
  const daChiarire = [...stato.repo.values()].filter((v) =>
    v.aggiorna && !v.installato.presente && (!v.ambito || v.scelta) && v.installabile !== "no" && !v.fase);
  try {
    for (const v of daChiarire) {
      if (v.scelta) { await aggiorna(v); continue; }
      const ambito = await chiediAmbito(v);
      if (ambito) await imposta(v, { ambito });
    }
    await api().AggiornaTutti();
  } catch (e) { barra(messaggio(e), true); }
}

$("btn-tutti").addEventListener("click", () => (stato.modo === "archivio" ? api().ArchiviaTutti() : aggiornaTutti()));
$("btn-controlla").addEventListener("click", () => api().ControllaTutti());

// ---- finestra del repository -------------------------------------------------

function mostraAmbito() {
  document.querySelectorAll("#repo-ambito button").forEach((b) => b.classList.toggle("active", b.dataset.ambito === stato.ambito));
}

function mostraChiave() {
  const priv = $("repo-privato").checked;
  $("repo-campo-chiave").hidden = !priv;
  const v = stato.inModifica ? stato.repo.get(stato.inModifica) : null;
  $("repo-chiave-nota").textContent = v && v.conChiave
    ? "C'è già una chiave salvata: lasciala vuota per tenerla, scrivine una per sostituirla."
    : "Si salva solo su questo computer, in un file che puoi leggere solo tu. Non viene mai mostrata.";
}

function apriRepo(v) {
  stato.inModifica = v ? v.id : null;
  $("repo-titolo").textContent = v ? v.nome : "Nuovo repository";
  $("repo-repo").value = v ? v.repo : "";
  $("repo-nome").value = v ? v.nome : "";
  $("repo-privato").checked = v ? v.privato : false;
  $("repo-chiave").value = "";
  mostraChiave();
  $("repo-tag").value = v ? v.tag : (stato.tag || "");
  $("repo-cartella").value = v ? v.cartellaArchivio : "";
  $("repo-aggiorna").checked = v ? v.aggiorna : false;
  $("repo-asset").value = v ? (v.asset || "") : "";
  stato.ambito = v ? v.ambito : "";
  mostraAmbito();
  $("repo-errore").hidden = true;
  $("dlg-repo").hidden = false;
  // chi arriva da «Aggiungi la chiave…» vuole scrivere la chiave
  (v && v.manca ? $("repo-chiave") : $("repo-repo")).focus();
}

$("repo-privato").addEventListener("change", mostraChiave);
$("btn-aggiungi").addEventListener("click", () => apriRepo(null));
$("repo-annulla").addEventListener("click", () => { $("dlg-repo").hidden = true; });
$("repo-ambito").addEventListener("click", (ev) => {
  const b = ev.target.closest("button[data-ambito]");
  if (!b) return;
  stato.ambito = stato.ambito === b.dataset.ambito ? "" : b.dataset.ambito; // un secondo clic toglie la scelta
  mostraAmbito();
});
$("repo-scegli").addEventListener("click", async () => {
  try {
    const d = await api().ScegliCartella("Cartella d'archivio", $("repo-cartella").value);
    if (d) $("repo-cartella").value = d;
  } catch (e) { /* finestra chiusa */ }
});
$("repo-salva").addEventListener("click", async () => {
  const l = { tag: $("repo-tag").value, cartellaArchivio: $("repo-cartella").value, aggiorna: $("repo-aggiorna").checked, ambito: stato.ambito };
  const privato = $("repo-privato").checked;
  try {
    const nuova = await api().SalvaRepo(Object.assign({
      id: stato.inModifica || "", nome: $("repo-nome").value, repo: $("repo-repo").value, asset: $("repo-asset").value,
      privato, token: privato ? $("repo-chiave").value : "",
    }, l));
    $("repo-chiave").value = "";
    metti(nuova);
    $("dlg-repo").hidden = true;
    ridisegna();
    api().Controlla(nuova.id);
  } catch (e) {
    $("repo-errore").textContent = messaggio(e);
    $("repo-errore").hidden = false;
  }
});

// ---- importa, esporta, registro ----------------------------------------------

// Esportare chiede se mettere nel file anche le chiavi: due pulsanti, nessuno
// preselezionato.
$("btn-esporta").addEventListener("click", () => {
  // le chiavi si contano dai repository che si vedono: il conteggio della panoramica si aggiorna solo all'avvio
  const n = [...stato.repo.values()].filter((v) => v.conChiave).length;
  $("esp-corpo").innerHTML = "Il file JSON contiene i repository, i tag e le impostazioni." +
    (n > 0
      ? `<br><br>Hai ${n === 1 ? "una chiave" : n + " chiavi"} di accesso. <strong>Con le chiavi</strong> il file è un backup completo, ma chiunque lo legga può usarle: va tenuto come una password. <strong>Senza le chiavi</strong> c'è tutto il resto, e chi lo importa vede quali repository ne chiedono una.`
      : `<br><br>Non hai salvato nessuna chiave di accesso: il file non ne conterrà.`);
  $("esp-con").disabled = n === 0;
  $("dlg-esporta").hidden = false;
});
$("esp-annulla").addEventListener("click", () => { $("dlg-esporta").hidden = true; });
async function esporta(conChiavi) {
  $("dlg-esporta").hidden = true;
  try {
    const f = await api().Esporta(conChiavi);
    if (f) barra("Configurazione esportata " + (conChiavi ? "con le chiavi" : "senza le chiavi") + " in " + f);
  } catch (e) { barra(messaggio(e), true); }
}
$("esp-senza").addEventListener("click", () => esporta(false));
$("esp-con").addEventListener("click", () => esporta(true));

$("btn-importa").addEventListener("click", async () => {
  const ok = await conferma("Importare una configurazione?",
    "I repository del file vengono <strong>aggiunti</strong> alla lista; quelli già presenti si completano con le impostazioni del file. Non si toglie niente, e una chiave che hai già resta se il file non ne porta una. I repository privati senza chiave compaiono subito, con l'avviso che manca. Della configurazione attuale resta una copia accanto al suo file (<strong>config.json.bak</strong>).",
    "Scegli il file…");
  if (!ok) return;
  try {
    const u = await api().Importa();
    if (!u) return;
    await ricarica();
    const parti = [];
    if (u.aggiunti) parti.push(u.aggiunti === 1 ? "aggiunto un repository" : "aggiunti " + u.aggiunti + " repository");
    if (u.aggiornati) parti.push(u.aggiornati === 1 ? "completato uno già presente" : "completati " + u.aggiornati + " già presenti");
    if (u.saltati) parti.push(u.saltati + " saltati perché un altro ha lo stesso nome");
    let testo = parti.length ? parti.join(", ") : "nessun repository nel file";
    testo = testo[0].toUpperCase() + testo.slice(1);
    if (u.senzaChiave) testo += ". " + (u.senzaChiave === 1 ? "Uno è privato e senza chiave" : u.senzaChiave + " sono privati e senza chiave") + ": aggiungila da «Aggiungi la chiave…».";
    barra(testo);
    api().ControllaTutti();
  } catch (e) { barra(messaggio(e), true); }
});

function registro(aperto) { $("log-panel").hidden = !aperto; }
$("btn-log").addEventListener("click", () => registro($("log-panel").hidden));
$("btn-log-chiudi").addEventListener("click", () => registro(false));
$("btn-log-pulisci").addEventListener("click", () => { $("log-body").textContent = ""; });

document.addEventListener("keydown", (e) => {
  if (e.key !== "Escape") return;
  if (!$("dlg-conferma").hidden) $("conf-annulla").click();
  else if (!$("dlg-esporta").hidden) $("esp-annulla").click();
  else if (!$("dlg-installa").hidden) $("inst-annulla").click();
  else if (!$("dlg-ambito").hidden) $("ambito-annulla").click();
  else if (!$("dlg-repo").hidden) $("dlg-repo").hidden = true;
});

// Un file lasciato cadere sulla finestra non deve sostituire l'interfaccia.
window.addEventListener("dragover", (e) => e.preventDefault());
window.addEventListener("drop", (e) => e.preventDefault());

// ---- avvio ---------------------------------------------------------------------

async function avvia() {
  window.runtime.EventsOn("repo", (v) => { metti(v); ridisegna(v.id); });
  window.runtime.EventsOn("log", riga);
  await ricarica();
  // All'apertura: c'è qualcosa da aggiornare?
  api().ControllaTutti();
}

avvia().catch((e) => barra("Avvio non riuscito: " + messaggio(e), true));
