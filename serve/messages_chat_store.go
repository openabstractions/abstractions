package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	cas "github.com/openabstractions/abstraction-cas/go"
	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	identity "github.com/openabstractions/abstraction-identity"
)

// The nonce and required wake counter share one atomic owner record. Expiring
// individual messages never replaces it. A reset creates a different owner.
type messageStoreOwner struct {
	Profile string
	Nonce   string
	Next    *uint64
	Pending *messageWakeReservation `json:",omitempty"`
}

func readMessageStoreOwner(raw []byte) (messageStoreOwner, error) {
	var owner messageStoreOwner
	if raw == nil {
		return owner, errors.New("messages: durable store owner missing")
	}
	if err := messageStoredJSON(raw, &owner); err != nil {
		return owner, err
	}
	if owner.Profile != "messages-owner@1" || !agentHex(owner.Nonce, 24) || owner.Next == nil {
		return owner, errors.New("messages: durable owner/counter corrupted")
	}
	return owner, nil
}

func (s *messageStore) ownerPath() string { return filepath.Join(s.dir, "sequences", "owner.json") }

func (s *messageStore) initializeOwner() error {
	token, err := applicationToken()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return cas.ChangeLimitContext(ctx, s.ownerPath(), 4096, func(raw []byte) ([]byte, error) {
		if raw == nil {
			raw, err = json.Marshal(messageStoreOwner{Profile: "messages-owner@1", Nonce: token, Next: new(uint64)})
			if err != nil {
				return nil, err
			}
		}
		owner, err := readMessageStoreOwner(raw)
		if err != nil {
			return nil, err
		}
		s.ownerNonce = owner.Nonce
		return raw, nil
	})
}

func (s *messageStore) changeOwner(ctx context.Context, edit func(*messageStoreOwner) error) error {
	return cas.ChangeLimitContext(ctx, s.ownerPath(), 4096, func(raw []byte) ([]byte, error) {
		owner, err := readMessageStoreOwner(raw)
		if err != nil {
			return nil, err
		}
		if owner.Nonce != s.ownerNonce {
			return nil, errors.New("messages: store owner changed")
		}
		if err := edit(&owner); err != nil {
			return nil, err
		}
		return json.Marshal(owner)
	})
}

type messageChatBindingRecord struct {
	Profile, Owner, Context       string
	Account, Program, Chat, Title string          `json:",omitempty"`
	Launch                        *msg.ChatLaunch `json:",omitempty"`
	CwdKey                        string          `json:",omitempty"`
	LastSeenUnixMs                int64           `json:"LastSeen,omitempty"`
}

const messageChatBindingLimit = 32 << 10

func messageChatLaunchKey(chat string, launch *msg.ChatLaunch) (string, error) {
	if launch == nil {
		return "", nil
	}
	if launch.Session != chat || len(launch.Session) > 128 || !filepath.IsAbs(launch.Cwd) || len(launch.Cwd) > 4096 || identity.LocalPath(launch.Cwd) != nil {
		return "", errors.New("messages: incomplete host launch correlation")
	}
	switch launch.Origin {
	case msg.ChatOriginDesktop, msg.ChatOriginCli:
	default:
		return "", errors.New("messages: unknown host origin")
	}
	switch launch.Mode {
	case msg.ChatModePlan, msg.ChatModeReadOnly, msg.ChatModeDefault, msg.ChatModeAcceptEdits:
	default:
		return "", errors.New("messages: unknown host mode")
	}
	info, err := os.Stat(launch.Cwd)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", errors.New("messages: host cwd is not a directory")
	}
	return identity.FileKey(launch.Cwd)
}

// chatContextName is the one chat-* name of a conversation: the bind path
// and discovery both name a conversation by it (architect ruling 1, AB-1s).
func (s *messageStore) chatContextName(account, program, chat string) string {
	return "chat-" + messageDigest(s.ownerNonce + "\x00" + account + "\x00" + program + "\x00" + chat)[:48]
}

func (s *messageStore) contextForChat(ctx context.Context, account, program, chat string, claims ...*msg.ChatBinding) (string, error) {
	if account == "" || program == "" || chat == "" {
		return "", errors.New("messages: chat binding missing")
	}
	key := messageDigest(account + "\x00" + program + "\x00" + chat)
	canonical := s.chatContextName(account, program, chat)
	var claim *msg.ChatBinding
	if len(claims) != 0 {
		claim = claims[0]
	}
	var cwdKey string
	if claim != nil {
		if claim.ChatID != chat {
			return "", errors.New("messages: host chat correlation changed")
		}
		var err error
		cwdKey, err = messageChatLaunchKey(chat, claim.Launch)
		if err != nil {
			return "", err
		}
	}
	err := s.changeOwner(ctx, func(owner *messageStoreOwner) error {
		return cas.ChangeLimitContext(ctx, filepath.Join(s.dir, "sequences", "chat-"+key+".json"), messageChatBindingLimit, func(raw []byte) ([]byte, error) {
			kept := messageChatBindingRecord{Profile: "messages-chat@1", Owner: owner.Nonce, Context: canonical}
			if raw != nil {
				if err := messageStoredJSON(raw, &kept); err != nil {
					return nil, err
				}
				if kept.Profile != "messages-chat@1" || kept.Owner != owner.Nonce || !applicationContextName.MatchString(kept.Context) {
					return nil, errors.New("messages: retained chat binding changed")
				}
				if kept.Account != "" && (kept.Account != account || kept.Program != program || kept.Chat != chat) {
					return nil, errors.New("messages: retained host correlation changed")
				}
				canonical = kept.Context
			}
			if claim == nil && raw != nil {
				return raw, nil
			}
			if claim != nil {
				kept.Account, kept.Program, kept.Chat, kept.Title = account, program, chat, nativeMessageHeader(claim.ChatTitle)
				kept.LastSeenUnixMs = s.now().UnixMilli()
				kept.Launch, kept.CwdKey = nil, ""
				if claim.Launch != nil {
					copy := *claim.Launch
					kept.Launch, kept.CwdKey = &copy, cwdKey
				}
			}
			return json.Marshal(kept)
		})
	})
	if err != nil {
		return "", err
	}
	return canonical, nil
}

// retainedChats inventories only OA's existing chat association records.
// A retained row proves correlation, never that its host is absent or that a
// start is admitted. Legacy rows without native metadata remain undiscovered.
func (s *messageStore) retainedChats(ctx context.Context, account, program string) ([]messageChatBindingRecord, error) {
	currentOwner := func() error {
		raw, err := cas.ReadLimitContext(ctx, s.ownerPath(), 4096)
		if err != nil {
			return err
		}
		owner, err := readMessageStoreOwner(raw)
		if err != nil {
			return err
		}
		if owner.Nonce != s.ownerNonce {
			return errors.New("messages: store owner changed")
		}
		return nil
	}
	if err := currentOwner(); err != nil {
		return nil, err
	}
	dir, err := os.Open(filepath.Join(s.dir, "sequences"))
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	rows := []messageChatBindingRecord{}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		entries, err := dir.ReadDir(64)
		if err != nil && err != io.EOF {
			return nil, err
		}
		for _, entry := range entries {
			name := entry.Name()
			if entry.IsDir() || !strings.HasPrefix(name, "chat-") || !strings.HasSuffix(name, ".json") {
				continue
			}
			raw, err := cas.ReadLimitContext(ctx, filepath.Join(s.dir, "sequences", name), messageChatBindingLimit)
			if err != nil {
				return nil, err
			}
			var kept messageChatBindingRecord
			if err := messageStoredJSON(raw, &kept); err != nil {
				return nil, err
			}
			if kept.Profile != "messages-chat@1" || kept.Owner != s.ownerNonce || !applicationContextName.MatchString(kept.Context) {
				return nil, errors.New("messages: retained chat binding changed")
			}
			if kept.Account != account || kept.Program != program || kept.Chat == "" {
				continue
			}
			if name != "chat-"+messageDigest(account+"\x00"+program+"\x00"+kept.Chat)+".json" {
				return nil, errors.New("messages: retained chat key changed")
			}
			rows = append(rows, kept)
		}
		if err == io.EOF {
			break
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Context < rows[j].Context })
	if err := currentOwner(); err != nil {
		return nil, err
	}
	return rows, nil
}

func (r messageChatBindingRecord) currentLaunch() (*msg.ChatLaunch, error) {
	if r.Launch == nil || r.CwdKey == "" {
		return nil, errors.New("messages: host launch facts missing")
	}
	key, err := messageChatLaunchKey(r.Chat, r.Launch)
	if err != nil {
		return nil, err
	}
	if key != r.CwdKey {
		return nil, errors.New("messages: original host cwd changed")
	}
	copy := *r.Launch
	return &copy, nil
}
