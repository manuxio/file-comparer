# file-comparer

[![build](https://github.com/manuxio/file-comparer/actions/workflows/build.yml/badge.svg)](https://github.com/manuxio/file-comparer/actions/workflows/build.yml)

**Italiano** · [English](README.en.md)

> **Toolkit forense per il rilevamento di manomissioni.** Due piccole app Go, ognuna
> in un container Docker, che individuano *quali* file sono cambiati tra lo stato
> attuale di un sistema (potenzialmente compromesso) e l'ultimo backup integro — così
> il confronto approfondito dei contenuti va fatto solo sui file che sono davvero
> diversi, non sull'intero disco.

- **`checksum`** (App 1) — calcola ricorsivamente l'hash di ogni file, sotto una
  directory radice, la cui estensione è nell'elenco consentito, e scrive un
  **manifest CSV deterministico**.
- **`csvdiff`** (App 2) — confronta due manifest (un *baseline* e un *current*) e
  scrive un CSV con i soli file **modificati**, **aggiunti** o **eliminati**.

---

## Indice

- [A cosa serve](#a-cosa-serve)
- [Come funziona: il flusso di incident response](#come-funziona-il-flusso-di-incident-response)
- [Avvio rapido](#avvio-rapido)
- [Immagini Docker ed esempi](#immagini-docker-ed-esempi)
- [Formati CSV](#formati-csv)
- [Opzioni di configurazione](#opzioni-di-configurazione)
- [Prestazioni e tuning](#prestazioni-e-tuning)
- [Codici di uscita](#codici-di-uscita)
- [Sicurezza](#sicurezza)
- [Sviluppo](#sviluppo)

---

## A cosa serve

Durante (o dopo) un sospetto attacco, la domanda a cui rispondere in fretta è:
**quali file sono stati alterati rispetto all'ultimo backup buono?**

Calcolare l'hash di ogni file e confrontare i due elenchi di hash è enormemente
più veloce che confrontare i contenuti, e permette di restringere l'analisi
manuale ai soli file effettivamente diversi. È esattamente ciò che fa questo
toolkit:

| App | Ruolo |
|---|---|
| **`checksum`** | Produce un *manifest* (CSV di hash) di un albero di directory. |
| **`csvdiff`** | Confronta due manifest (baseline vs current) ed elenca i file aggiunti / eliminati / modificati. |

Entrambe sono distribuite come immagini Docker minimali, pensate per essere
eseguite contro dischi montati (es. l'immagine di un disco sotto analisi).

## Come funziona: il flusso di incident response

```text
1. checksum  → baseline.csv   (eseguito sull'ultimo backup integro)
2. checksum  → current.csv    (eseguito sul sistema attuale / sospetto)
3. csvdiff   → changes.csv    (baseline vs current)
4. si esamina changes.csv, poi si confronta il contenuto SOLO dei file segnalati
```

Il disco di origine viene sempre montato in **sola lettura**: lo strumento non
modifica mai il sistema sotto indagine.

## Avvio rapido

Vuoi vederlo funzionare subito, senza toccare dati reali? Questo blocco è
**autosufficiente**: crea dei file di esempio nella cartella temporanea, esegue
l'intera pipeline e stampa il risultato. Copialo e incollalo in **PowerShell**:

```powershell
$w = "$env:TEMP\fsd-try"
New-Item -ItemType Directory -Force -Path "$w\backup","$w\current","$w\out" | Out-Null
[IO.File]::WriteAllText("$w\backup\index.php",  "homepage v1")
[IO.File]::WriteAllText("$w\backup\app.js",     "console.log('v1');")
[IO.File]::WriteAllText("$w\backup\legacy.php", "vecchia pagina")
[IO.File]::WriteAllText("$w\current\index.php", "homepage v1")
[IO.File]::WriteAllText("$w\current\app.js",    "console.log('v2 modificato');")
[IO.File]::WriteAllText("$w\current\added.php", "file nuovo")
$d = $w -replace '\\','/'
docker run --rm -v "${d}/backup:/mnt/data:ro"  -v "${d}/out:/out" -e EXTENSIONS=".php,.js" -e OUTPUT=/out/baseline.csv ghcr.io/manuxio/file-comparer/checksum
docker run --rm -v "${d}/current:/mnt/data:ro" -v "${d}/out:/out" -e EXTENSIONS=".php,.js" -e OUTPUT=/out/current.csv  ghcr.io/manuxio/file-comparer/checksum
docker run --rm -v "${d}/out:/data:ro" -v "${d}/out:/out" -e BASELINE_CSV=/data/baseline.csv -e CURRENT_CSV=/data/current.csv -e OUTPUT=/out/changes.csv ghcr.io/manuxio/file-comparer/csvdiff
Write-Host "`n----- changes.csv -----"; Get-Content "$w\out\changes.csv"
```

Risultato atteso: `added.php` come **ADDED**, `legacy.php` come **DELETED**,
`app.js` come **MODIFIED**; `index.php` (invariato) viene correttamente omesso.

> Se stai usando le immagini locali (build da sorgente) anziché GHCR, sostituisci
> `ghcr.io/manuxio/file-comparer/checksum` con il tag locale, es. `fsd-checksum:test`.

## Immagini Docker ed esempi

Le immagini vengono pubblicate su GHCR a ogni push su `main`:

- `ghcr.io/manuxio/file-comparer/checksum`
- `ghcr.io/manuxio/file-comparer/csvdiff`

Sono immagini `distroless/static:nonroot` (nessuna shell, nessun package manager,
eseguite come utente non-root, uid 65532).

### Eseguire `checksum`

```bash
docker run --rm \
  -v /percorso/del/disco:/mnt/data:ro \
  -v "$PWD/out":/out \
  -e EXTENSIONS=".php,.js,.phtml,.html" \
  -e OUTPUT=/out/current.csv \
  ghcr.io/manuxio/file-comparer/checksum
```

Su **PowerShell** (Windows), usando percorsi con `/` e apici doppi:

```powershell
docker run --rm `
  -v "D:/evidenze/disco:/mnt/data:ro" `
  -v "D:/out:/out" `
  -e EXTENSIONS=".php,.js,.exe,.dll,.aspx" `
  -e OUTPUT=/out/current.csv `
  ghcr.io/manuxio/file-comparer/checksum
```

### Eseguire `csvdiff`

```bash
docker run --rm \
  -v "$PWD/out":/data:ro \
  -v "$PWD/out":/out \
  -e BASELINE_CSV=/data/baseline.csv \
  -e CURRENT_CSV=/data/current.csv \
  -e OUTPUT=/out/changes.csv \
  ghcr.io/manuxio/file-comparer/csvdiff
```

> **Permessi:** il disco di origine è montato in sola lettura (`:ro`) — lo
> strumento non modifica mai il disco sotto indagine. La cartella di output deve
> essere scrivibile dall'uid 65532; per un'analisi usa-e-getta in cui questo è
> scomodo, aggiungi `--user root`.

### docker-compose

```bash
SCAN_DIR=/percorso/del/disco OUT_DIR=./out docker compose run --rm checksum
CSV_DIR=./out                OUT_DIR=./out docker compose run --rm csvdiff
```

## Formati CSV

**Manifest** (output di `checksum`):

```text
absolute_path,filename,last_modified,size_bytes,sha256
```

- `last_modified`: formato RFC3339 in UTC (es. `2026-07-07T03:10:00Z`).
- Le righe sono **ordinate per `absolute_path`**: due esecuzioni sullo stesso
  albero producono file identici byte per byte (comodo per il versionamento e i
  diff).

**Changes** (output di `csvdiff`):

```text
status,absolute_path,filename,baseline_sha,current_sha,baseline_size,current_size,baseline_modified,current_modified
```

- `status` vale `MODIFIED`, `ADDED` (presente solo nel current — spesso il più
  interessante, es. una webshell caricata) oppure `DELETED` (presente solo nel
  baseline). I file invariati vengono omessi.

## Opzioni di configurazione

Ogni opzione si può passare come **flag** della riga di comando *oppure* come
**variabile d'ambiente** (il flag ha la precedenza). In Docker si usano
tipicamente le variabili d'ambiente (`-e`).

### `checksum`

| Flag | Variabile | Default | Significato |
|---|---|---|---|
| `--root` | `SCAN_ROOT` | `/mnt/data` | Albero di directory da analizzare. |
| `--ext` (ripetibile) | `EXTENSIONS` (separate da virgola) | *(obbligatorio)* | Estensioni da includere, confrontate senza distinzione tra maiuscole/minuscole. |
| `--output` | `OUTPUT` | *(obbligatorio)* | Percorso del CSV di output. |
| `--algo` | `ALGO` | `sha256` | `sha256` oppure `sha512`. |
| `--follow-symlinks` | `FOLLOW_SYMLINKS` | `false` | Segue i symlink verso file regolari (mai verso directory). |
| `--fail-fast` | `FAIL_FAST` | `false` | Si ferma al primo file illeggibile invece di proseguire. |
| `--max-depth` | `MAX_DEPTH` | `0` | Livelli massimi di directory sotto la radice (0 = illimitato; le voci nella radice sono a profondità 1). Le directory potate vengono stampate su stderr. |
| `--workers` | `WORKERS` | `0` | Concorrenza di hashing (0 = numero di CPU). Più bassa per un singolo HDD, più alta per NVMe/rete. |
| `--dir-workers` | `DIR_WORKERS` | `0` | Concorrenza di lettura directory (0 = come `--workers`). Da alzare su filesystem di rete/ad alta latenza con molte directory. |

### `csvdiff`

| Flag | Variabile | Significato |
|---|---|---|
| `--baseline` | `BASELINE_CSV` | Manifest del baseline (ultimo backup integro). |
| `--current` | `CURRENT_CSV` | Manifest del sistema attuale/sospetto. |
| `--output` | `OUTPUT` | CSV di output con le differenze. |
| `--strip-baseline-prefix` / `--strip-current-prefix` | `STRIP_BASELINE_PREFIX` / `STRIP_CURRENT_PREFIX` | Rimuove un prefisso di percorso prima del confronto, per manifest acquisiti con punti di mount diversi (es. `/mnt/backup` vs `/mnt/data`). |
| `--fail-on-diff` | `FAIL_ON_DIFF` | Restituisce codice `3` se viene trovata qualsiasi differenza (utile per automazioni/allarmi). |

Esempio di normalizzazione dei prefissi, quando backup e sistema attuale sono
stati acquisiti in punti di mount diversi:

```bash
docker run --rm -v "$PWD/out":/data:ro -v "$PWD/out":/out \
  -e BASELINE_CSV=/data/baseline.csv -e CURRENT_CSV=/data/current.csv \
  -e OUTPUT=/out/changes.csv \
  -e STRIP_BASELINE_PREFIX=/mnt/backup -e STRIP_CURRENT_PREFIX=/mnt/data \
  ghcr.io/manuxio/file-comparer/csvdiff
```

## Prestazioni e tuning

Pensato per scansioni da **più TB**. Entrambe le fasi girano in parallelo:

- **Hashing** — un pool di `--workers` goroutine (default = numero di CPU), ognuna
  legge un file in streaming attraverso l'hash con un buffer riutilizzabile da
  1 MiB (meno syscall di lettura sui file grandi).
- **Traversata delle directory** — un pool separato di `--dir-workers` goroutine
  che leggono le directory in parallelo, così la traversata non è più un collo di
  bottiglia a thread singolo su storage ad alta latenza o ad alto parallelismo.
- Le due fasi sono **disaccoppiate da una coda limitata**: la backpressure mantiene
  costante l'uso di memoria anche con milioni di file.

Il throughput di hashing scala in modo quasi lineare con i worker fino al numero
di core (misurato su ~750 MB / 1500 file, con cache calda):

| `--workers` | tempo | speedup |
|---|---|---|
| 1  | 510 ms | 1,0× |
| 2  | 247 ms | 2,1× |
| 4  | 125 ms | 4,1× |
| 8  | 69 ms  | 7,4× |
| 16 | 51 ms  | 10×  |

### Regole pratiche per il tuning

| Tipo di storage | Consiglio |
|---|---|
| **NVMe / SSD / RAID / SAN** | Alza `--workers` (es. `16`) — la concorrenza satura la banda. |
| **Mount di rete / immagine disco** | Alza soprattutto `--dir-workers` — nasconde la latenza di ogni `readdir`. |
| **Singolo HDD meccanico** | *Abbassa* `--workers` (prova `2`) — le letture parallele fanno "sbattere" la testina; conviene la lettura quasi-sequenziale. |

> SHA-256 è accelerato via hardware sulle CPU moderne: il carico è quasi sempre
> **limitato dall'I/O**, non dalla CPU. In pratica il collo di bottiglia è il disco:
> adatta il numero di worker al tipo di storage.

Esempio per un array veloce:

```powershell
docker run --rm -v "D:/evidenze:/mnt/data:ro" -v "D:/out:/out" `
  -e EXTENSIONS=".php,.js,.exe,.dll,.aspx" -e OUTPUT=/out/manifest.csv `
  -e WORKERS=16 -e DIR_WORKERS=8 `
  ghcr.io/manuxio/file-comparer/checksum
```

La riga di riepilogo su stderr riporta i valori effettivi usati
(`workers=16 dir-workers=8 …`), utile per confrontare le configurazioni.

> **Consiglio per il primo run su più TB:** i numeri qui sopra sono a cache calda,
> quindi misurano il throughput di hashing/traversata, non la lettura da disco a
> freddo. Su una scansione reale a freddo domina la velocità del disco: conviene
> fare una prova di tuning su una sottocartella rappresentativa (prova
> `--workers 4`, `8`, `16`) e osservare `elapsed=` per trovare il valore ottimale
> per *il tuo* storage prima della scansione completa.

## Codici di uscita

**`checksum`**

| Codice | Significato |
|---|---|
| `0` | Completato; ogni file corrispondente è stato hashato correttamente. |
| `1` | Errore fatale di configurazione/avvio; nessun manifest prodotto. |
| `2` | Manifest prodotto, ma uno o più file non sono stati letti (dettagli su stderr). |

**`csvdiff`**

| Codice | Significato |
|---|---|
| `0` | Confronto riuscito (0 a meno che non si usi `--fail-on-diff` con differenze presenti). |
| `1` | Errore fatale (file di input mancante/illeggibile, flag errati). |
| `2` | Un CSV di input non ha superato la validazione dello schema. |
| `3` | Confronto riuscito **e** differenze trovate (solo con `--fail-on-diff`). |

Gli errori di lettura dei singoli file non vengono **mai** ignorati in silenzio:
vengono stampati su stderr e conteggiati nel riepilogo.

## Sicurezza

- **Origine in sola lettura:** i mount del disco usano `:ro`; il disco sotto
  indagine non viene mai modificato.
- **Container non-root, distroless:** nessuna shell né package manager, superficie
  d'attacco minima, esecuzione come uid 65532.
- **SHA-256 di default** (o SHA-512): resistente alle collisioni, adatto al
  rilevamento di manomissioni. Non vengono offerti MD5/SHA-1 per l'uso di sicurezza.
- **Nessun ciclo infinito:** i symlink non vengono mai attraversati (né in caso di
  cicli di symlink), viene percorso solo l'albero reale delle directory; `--max-depth`
  è un ulteriore limite per casi esotici (cicli reali creati da bind/loop mount) e
  ogni directory potata viene stampata (mai un limite silenzioso).

## Sviluppo

Non serve installare Go in locale se hai Docker:

```bash
docker run --rm -v "$PWD":/src -w /src golang:1.23 go test ./...
docker run --rm -v "$PWD":/src -w /src golang:1.23 go vet ./...
```

Con Go 1.23+ installato in locale:

```bash
go test ./...
go build ./...
```

Vedi [CLAUDE.md](CLAUDE.md) per architettura e convenzioni e [PLAN.md](PLAN.md)
per le decisioni di progetto e la roadmap.
