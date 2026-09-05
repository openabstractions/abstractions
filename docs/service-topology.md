# One service or many

How services are addressed, so that whether one process serves everything or a
dozen processes serve one thing each is a deployment decision nobody's code
depends on.

Status: **specified, unimplemented.** This constrains `docs/discovery-ipc.md`
and everything built on it.

---

## The requirement that decides everything

Switching between one process and many must be trivial. That is only possible if
**the number of processes never appears in the contract.** Everything else here
follows from taking that literally.

So: a caller asks for a *service* and receives a *reference*. It never asks for
a process, never learns a process, and cannot tell how many there are. A
deployment that merges two services and one that splits them are
indistinguishable to every client.

This is the same rule the download layer already lives by — an application asks
for bytes and never chooses who fetches them. Here the thing being hidden is
process topology rather than transport.

## The registry

The store holds a registry: service name to endpoint.

```json
{
  "services": {
    "abstraction.jobs":     "abstraction-3f9a1c04e7b25d8613ca9f2076be4481",
    "abstraction.downloads":"abstraction-3f9a1c04e7b25d8613ca9f2076be4481",
    "abstraction.rights":   "abstraction-b71e5ff0294c8a63d5107ea4c9820fd6"
  }
}
```

One process serving jobs and downloads publishes one endpoint under two names.
Split them tomorrow and it publishes two. **The client code is identical**, and
no caller can tell which happened. That is the whole mechanism.

A client resolves a service name to an endpoint, connects, and asks. It must
never assume two names sharing an endpoint are related, or that two names with
different endpoints are unrelated.

## Endpoint conflict

There is none, by construction. An endpoint is 128 random bits chosen by
whoever binds it, not a name derived from anything. Two services cannot collide,
two stores on a machine cannot collide, and a hostile local process cannot
predict a name to squat.

The registry entry is the only way to learn an endpoint, so being able to read
the store is what confers the reference. That is a capability, and it arrives
here for free rather than as a mechanism anyone had to design.

Two writers of one registry is the real hazard, not two endpoints. A service
claims its name with the same discipline the job store already uses for a lease:
create exclusively, and refuse to start if the name is held and its endpoint
answers. If the name is held and its endpoint does not answer, the holder is
gone and the name may be taken.

## Where to split, and it is not a count

"One service or many" is the wrong question. **Split where privileges differ, merge
where they do not.**

| service | needs | may share a process with |
|---|---|---|
| downloads | network, write access to wherever models land | jobs |
| jobs | the store | downloads |
| logging, config | almost nothing | anything |
| **rights** | **the ability to issue authority** | **nothing** |

A downloader compromised through a malicious URL is bad. A downloader that
shares a process with the thing issuing tokens is a downloader that can forge
authority, and that is a different category of bad. So the rule is not a number:

> A service that grants authority never shares a process with one that parses
> untrusted input.

Everything else may be merged for convenience, and merging must remain
reversible without touching a caller.

## Tokens

An application says what it needs. It is granted a reference to exactly that and
nothing else, and it presents that reference to the service it wants to use. It
cannot ask for what it was not handed, which removes the policy table and the
question "may this app do that" along with it.

That means a token is a capability, not a claim about identity. Nothing anywhere
may make a decision based on a name in a response — those are self-description
and always forgeable in principle. The reference is the authority.

Platform identity re-enters exactly once, at bootstrap: something has to decide
which references a given application is handed the first time. That is where
`research/integration/caller-identity.txt` applies, and it is the only place it
does.

## What this forbids

- A client that behaves differently depending on how many endpoints it sees.
- A service that publishes its own process id, port, or path as an address.
- Any endpoint name derived from something a caller could compute.
- A registry entry pointing at a machine that is not this one. Cross-machine
  discovery stays with the shared store, which is the thing that crosses.
- Merging the rights service into anything.
