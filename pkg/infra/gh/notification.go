package gh

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/m-mizutani/octify/pkg/domain/model"
	"github.com/m-mizutani/octify/pkg/domain/types"
	"github.com/m-mizutani/octify/pkg/utils/safe"
)

// MaxPerPage is the largest per_page GitHub accepts for the notifications list.
const MaxPerPage = 50

type ListNotificationsInput struct {
	All           bool
	Participating bool
	// LastModified is the previous response's Last-Modified value. When set, the
	// request is conditional and may come back as 304.
	LastModified string
	PerPage      int
	Page         int
}

type ListNotificationsOutput struct {
	Notifications []model.Notification
	NotModified   bool
	LastModified  string
	// PollInterval is x-poll-interval, or zero when absent or malformed.
	PollInterval time.Duration
	// NextPage is 0 when there is no further page.
	NextPage int
}

type apiNotification struct {
	ID        string `json:"id"`
	Unread    bool   `json:"unread"`
	Reason    string `json:"reason"`
	UpdatedAt string `json:"updated_at"`
	Subject   struct {
		Title string `json:"title"`
		URL   string `json:"url"`
		Type  string `json:"type"`
	} `json:"subject"`
	Repository struct {
		FullName string `json:"full_name"`
		HTMLURL  string `json:"html_url"`
		Private  bool   `json:"private"`
	} `json:"repository"`
}

func (c *Client) ListNotifications(ctx context.Context, in ListNotificationsInput) (*ListNotificationsOutput, error) {
	perPage := in.PerPage
	if perPage <= 0 || perPage > MaxPerPage {
		perPage = MaxPerPage
	}
	page := in.Page
	if page <= 0 {
		page = 1
	}

	q := url.Values{}
	q.Set("all", strconv.FormatBool(in.All))
	q.Set("participating", strconv.FormatBool(in.Participating))
	q.Set("per_page", strconv.Itoa(perPage))
	q.Set("page", strconv.Itoa(page))

	req, err := c.newRequest(ctx, http.MethodGet, "/notifications", q)
	if err != nil {
		return nil, err
	}
	if in.LastModified != "" {
		req.Header.Set("If-Modified-Since", in.LastModified)
	}

	resp, err := c.do(req, "notifications")
	if err != nil {
		return nil, err
	}
	defer safe.Close(ctx, resp.Body)

	if resp.StatusCode == http.StatusNotModified {
		return &ListNotificationsOutput{
			NotModified:  true,
			LastModified: in.LastModified,
			PollInterval: pollInterval(resp.Header),
		}, nil
	}

	var raw []apiNotification
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, invalidResponse(err, "notifications")
	}

	out := &ListNotificationsOutput{
		Notifications: make([]model.Notification, 0, len(raw)),
		LastModified:  resp.Header.Get("Last-Modified"),
		PollInterval:  pollInterval(resp.Header),
		NextPage:      nextPage(resp.Header),
	}
	for _, item := range raw {
		n, ok := convertNotification(item)
		if !ok {
			// A single malformed entry must not cost the whole page.
			continue
		}
		out.Notifications = append(out.Notifications, n)
	}
	return out, nil
}

func convertNotification(item apiNotification) (model.Notification, bool) {
	if item.ID == "" {
		return model.Notification{}, false
	}

	// An unparsable timestamp becomes the zero value: the row still shows, with
	// a placeholder instead of a relative time.
	var updatedAt time.Time
	if item.UpdatedAt != "" {
		if t, err := time.Parse(time.RFC3339, item.UpdatedAt); err == nil {
			updatedAt = t
		}
	}

	return model.Notification{
		ID: types.ThreadID(item.ID),
		Repo: model.Repository{
			FullName: types.RepoFullName(item.Repository.FullName),
			HTMLURL:  item.Repository.HTMLURL,
			Private:  item.Repository.Private,
		},
		Subject: model.Subject{
			Title:  item.Subject.Title,
			Type:   types.SubjectType(item.Subject.Type),
			URL:    item.Subject.URL,
			Number: subjectNumber(item.Subject.URL),
		},
		Reason:       model.Reason(item.Reason),
		ServerUnread: item.Unread,
		UpdatedAt:    updatedAt,
	}, true
}

// subjectNumber reads the trailing path segment as a decimal number, which is
// how pull request and issue numbers appear in the subject URL.
func subjectNumber(rawURL string) int {
	if rawURL == "" {
		return 0
	}
	idx := strings.LastIndex(rawURL, "/")
	if idx < 0 || idx == len(rawURL)-1 {
		return 0
	}
	n, err := strconv.Atoi(rawURL[idx+1:])
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

// MarkThreadDone removes the notification from the inbox, which is what the web
// UI calls "Done".
func (c *Client) MarkThreadDone(ctx context.Context, id types.ThreadID) error {
	req, err := c.newRequest(ctx, http.MethodDelete, "/notifications/threads/"+url.PathEscape(string(id)), nil)
	if err != nil {
		return err
	}

	resp, err := c.do(req, "notifications")
	if err != nil {
		return err
	}
	defer safe.Close(ctx, resp.Body)
	return nil
}
