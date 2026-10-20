package main

import "context"

// Same-app messaging reaches only the chats of the sender's own project
// (coordinator decision E2 on the architect ruling 2026-11-09, AB-1s): the
// main working folder of a Git repository, or the folder outside Git. The
// owner asked for teams "at least for given project / folder". Cross-app
// scope is unchanged.

// messageSameAppProjects maps each known conversation of one app to its
// project: a retained chat's launch folder, otherwise its discovered row.
// A conversation of unknown project is absent.
func (h *applicationsHost) messageSameAppProjects(ctx context.Context, application, program string) map[string]string {
	out := map[string]string{}
	if h.directory == nil || application == "" {
		return out
	}
	for _, s := range h.directory.messageAttachSessionsFor(ctx, application) {
		if s.Project != "" {
			out[s.Context] = s.Project
		}
	}
	if h.messages != nil {
		if rows, err := h.messages.retainedChats(ctx, h.directory.owner, program); err == nil {
			for _, row := range rows {
				if row.Launch == nil || row.Launch.Cwd == "" {
					continue
				}
				if project := messageProject(row.Launch.Cwd); project != "" {
					out[row.Context] = project
				}
			}
		}
	}
	return out
}

// messageSameAppSenderProject is the sender's project among projects: its own
// return context first, then any conversation it owns.
func messageSameAppSenderProject(actor messageActor, projects map[string]string, owned map[string]bool) string {
	if project := projects[actor.returnContext()]; project != "" {
		return project
	}
	for contextName := range owned {
		if project := projects[contextName]; project != "" {
			return project
		}
	}
	return ""
}

// messageSameAppInProject admits a same-app target conversation in the
// sender's project; an unknown project on either side admits nothing.
func (h *applicationsHost) messageSameAppInProject(ctx context.Context, actor messageActor, program, contextName string) bool {
	projects := h.messageSameAppProjects(ctx, actor.Application, program)
	sender := messageSameAppSenderProject(actor, projects, nil)
	return sender != "" && projects[contextName] == sender
}
