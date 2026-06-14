package api

import (
	"encoding/json"
	"net/url"
)

type AccessGrant struct {
	ID        string `json:"id"`
	IP        string `json:"ip"`
	Note      string `json:"note"`
	CreatedBy string `json:"createdBy"`
	CreatedAt string `json:"createdAt"`
	ExpiresAt string `json:"expiresAt"`
}

func (c *Client) ListAccessGrants() ([]AccessGrant, error) {
	var out struct {
		Grants []AccessGrant `json:"grants"`
	}
	if err := c.get("/api/access/grants", &out); err != nil {
		return nil, err
	}
	return out.Grants, nil
}

func (c *Client) CreateAccessGrant(ip, note, ttl string) (*AccessGrant, error) {
	body, _ := json.Marshal(map[string]string{"ip": ip, "note": note, "ttl": ttl})
	var grant AccessGrant
	if err := c.postJSON("/api/access/grants", string(body), &grant); err != nil {
		return nil, err
	}
	return &grant, nil
}

func (c *Client) DeleteAccessGrant(id string) error {
	return c.del("/api/access/grants/" + url.PathEscape(id))
}
