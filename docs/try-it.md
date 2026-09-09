# Try it: one command, three downloaders

The `dl` command line is byte-identical in all three parts below — no flag, no
path, no hostname, no mention of BITS or a NAS. Only what happens to be running
on the machine changes.

**Before you start.** Build `dl` and `jobd` from
[`abstraction-download`](https://github.com/openabstractions/abstraction-download):

```bash
git clone git@github.com:openabstractions/abstraction-download.git
cd abstraction-download/go
go build -o bin/dl ./cmd/dl
go build -o bin/jobd ./cmd/jobd
```

Part 1 runs anywhere Go does. Part 2 needs Windows, for BITS. Part 3 needs a
NAS running `jobd` — see `scripts/nas-deploy.sh` in the charter repository.

```
store        %USERPROFILE%\.abstraction
model        Qwen2.5-0.5B-Instruct-IQ2_M.gguf, 313 MB, from HuggingFace
```

The model is a public file, reachable from this PC *and* from the NAS, which
matters because in part 3 the machine doing the fetching is not this one.

---

## Part 1 — here

Nothing else is running, so `dl` does it in its own process.

```bash
./bin/dl.exe tiers
```

Expect `supervisor none`, and a note that downloads run in this process and stop
when it does. That is the bottom of the chain, not a failure.

```bash
./bin/dl.exe https://huggingface.co/bartowski/Qwen2.5-0.5B-Instruct-GGUF/resolve/main/Qwen2.5-0.5B-Instruct-IQ2_M.gguf -o ~/Downloads/
```

It prints `fetched by here` and a progress line.

**Now Ctrl+C it, halfway.** Then:

```bash
./bin/dl.exe list
```

The job is still there with its progress. Nothing about it lived in the process
you just killed.

Now run the **exact same** `dl` command again. Two things to look at:

- the job id printed is the **same one**, not a new job. Asking twice for the
  same artifact at the same destination is one piece of work, and repeating the
  command is how a person resumes.
- it starts from the byte the dead process had **proven**, not the byte it had
  written. Those are different numbers, and the difference is discarded because
  nothing vouched for it.

---

## Part 2 — the system downloader (BITS)

Start a supervisor in the background. `--without nas` runs it one tier lower
than this machine would otherwise choose, so you can see what the next one down
actually does — on Windows that is the OS transfer service.

```bash
./bin/jobd.exe start --without nas
```

It prints what it became and where its log is. It is detached: closing this
terminal does not stop it. Check on it any time with a bare:

```bash
./bin/jobd.exe
```

which tells you whether one is running, what it delegates to, and what is in
flight. `./bin/jobd.exe stop` ends it; `start` again replaces it rather than
stacking a second one.

Now, in any terminal:

```bash
./bin/dl.exe tiers
```

Now it says `supervisor jobd@...` and `which itself delegates to "bits"`.

```bash
./bin/dl.exe https://huggingface.co/bartowski/Qwen2.5-0.5B-Instruct-GGUF/resolve/main/Qwen2.5-0.5B-Instruct-IQ2_M.gguf -o ~/Downloads/
```

Same line as before. This time it prints `fetched by bits`.

**Now kill it — Ctrl+C, or close the whole terminal.** The download does not
stop. BITS is a Windows service; it is running under its own account with
nothing of yours attached to it.

```bash
./bin/dl.exe list
```

The numbers keep climbing with nothing of yours running.

---

## Part 3 — the NAS

Same thing, except this time the supervisor reads your real configuration, finds
`nas_store`, and hands the work to a machine that is not this one. Drop the flag
and it replaces the running one:

```bash
./bin/jobd.exe start
```

```bash
./bin/dl.exe tiers
```

Now: `which itself delegates to "nas"`.

```bash
./bin/dl.exe https://huggingface.co/bartowski/Qwen2.5-0.5B-Instruct-GGUF/resolve/main/Qwen2.5-0.5B-Instruct-IQ2_M.gguf -o ~/Downloads/
```

`fetched by nas`. Kill `dl` immediately if you like — it changes nothing.

To watch the far side, look at the record appearing in the NAS's own store:

```bash
ls -lt //<nas-host>/docker/abstraction/store/jobs/ | head -3
```

The NAS fetches the bytes over its own connection, proves the digest, and marks
the record transferred. `jobd` here notices, brings the file across, and puts it
where you asked.

---

## What to notice

The three `dl` invocations are the same string. Diff them.

`dl` does not import `bits` and does not import `nas`. Check it:

```bash
grep -nE "abstraction-download/(bits|nas)" go/cmd/dl/main.go || echo "neither is imported"
```

The words appear twice in that file and both times in prose — a comment saying
it imports no tier, and a usage line naming the setup command. The only question
`dl` asks is answered by the machine, not by you.

## Live view

```bash
./bin/dl.exe watch
```

Everything in flight, whoever is doing it, refreshed from the records — not from
memory. Open it in a third terminal during any of the parts above.
