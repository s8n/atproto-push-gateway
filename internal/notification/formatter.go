// Package notification renders push-notification titles and bodies.
// The functions here are pure: no I/O, no state, trivially testable.
package notification

import "fmt"

const zwsp = "​"

var titleTemplates = map[string]string{
	"like":              "%s liked your post",
	"repost":            "%s reposted your post",
	"reply":             "%s replied to your post",
	"mention":           "%s mentioned you",
	"quote":             "%s quoted your post",
	"follow":            "%s followed you",
	"like-via-repost":   "%s liked a post you reposted",
	"repost-via-repost": "%s reposted a post you reposted",
	"verified":          "Your account has been verified",
	"unverified":        "Your account verification was removed",
}

// Format renders the user-facing title and body for a push notification.
// Pure; no I/O. baseOverhead is the byte size of the final push payload
// with Title and Body both set to empty strings — the caller measures it
// via json.Marshal of the throwaway push.Notification.
func Format(reason, actorDisplayName, actorHandle, postText string, hasEmbed bool, baseOverhead int) (title, body string) {
	title = renderTitle(reason, actorDisplayName, actorHandle)
	body = zwsp
	return
}

func renderTitle(reason, actorDisplayName, actorHandle string) string {
	tmpl, ok := titleTemplates[reason]
	if !ok {
		return "Notification"
	}
	if reason == "verified" || reason == "unverified" {
		return tmpl
	}
	actor := actorDisplayName
	if actor == "" {
		actor = actorHandle
	}
	if actor == "" {
		actor = "Someone"
	}
	return fmt.Sprintf(tmpl, actor)
}
