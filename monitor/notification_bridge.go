package main

// bridgeNotificationSurface shows notifications through the macOS launcher's
// UserNotifications center over the private launcher channel (ASK-N7). The
// launcher posts what it is given and returns each click's prepared
// arguments; Go records every outcome through the service.
type bridgeNotificationSurface struct{ bridge *nativeAsksBridge }

func (s bridgeNotificationSurface) Show(n shownNotification) error {
	return s.bridge.write(nativeAsksFrame{Kind: "notification", ID: n.ID, Notification: nativeNotificationOf(n)})
}

func (s bridgeNotificationSurface) Remove(id, _ string) error {
	return s.bridge.write(nativeAsksFrame{Kind: "notification-remove", ID: id})
}

// nativeNotificationOf prepares one notification: the app's text, the
// service-recorded sender as the subtitle the app cannot write (ASK-N2), each
// action's click arguments, and OA's Turn off as the last action.
func nativeNotificationOf(n shownNotification) *nativeNotification {
	out := &nativeNotification{Title: trayDialogText(n.Title), Body: trayDialogText(n.Body), Sender: trayDialogText(n.Sender),
		Open: notificationClickArgs(n.ID, "", n.Token, false), Actions: []nativeNotificationAction{}, Silently: n.Silently}
	for _, a := range n.Actions {
		out.Actions = append(out.Actions, nativeNotificationAction{Title: trayDialogText(a.Title), Args: notificationClickArgs(n.ID, a.ID, n.Token, false)})
	}
	out.Actions = append(out.Actions, nativeNotificationAction{Title: trayDialogText("Turn off notifications from " + n.Sender), Args: notificationClickArgs(n.ID, "", n.Token, true)})
	return out
}
