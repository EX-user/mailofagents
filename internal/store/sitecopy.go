package store

import (
	"encoding/json"
	"fmt"
	"strings"

	bolt "go.etcd.io/bbolt"
)

// Site copy: admin-configurable text for the three brand surfaces (v0.1.2,
// superior order). Everything lives in the meta bucket; empty value = use
// the built-in i18n default. Chinese and English are configured separately;
// the frontend picks by active language.
type SiteCopy struct {
	PortalTaglineZh string `json:"portal_tagline_zh,omitempty"`
	PortalTaglineEn string `json:"portal_tagline_en,omitempty"`
	PortalTitleZh   string `json:"portal_title_zh,omitempty"`
	PortalTitleEn   string `json:"portal_title_en,omitempty"`
	PanelTitleZh    string `json:"panel_title_zh,omitempty"`
	PanelTitleEn    string `json:"panel_title_en,omitempty"`
}

const maxSiteCopyLen = 200

// SetSiteCopy persists any non-empty fields; empty fields leave existing
// values untouched (partial updates). Values are length-capped free text —
// the frontend renders them as text, never as HTML.
func (s *Store) SetSiteCopy(sc SiteCopy) error {
	fields := map[string]string{
		"site_portal_tagline_zh": sc.PortalTaglineZh,
		"site_portal_tagline_en": sc.PortalTaglineEn,
		"site_portal_title_zh":   sc.PortalTitleZh,
		"site_portal_title_en":   sc.PortalTitleEn,
		"site_panel_title_zh":    sc.PanelTitleZh,
		"site_panel_title_en":    sc.PanelTitleEn,
	}
	for k, v := range fields {
		if len(v) > maxSiteCopyLen {
			return fmt.Errorf("site copy %s too long (max %d)", k, maxSiteCopyLen)
		}
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		mb := tx.Bucket(bMeta)
		if mb == nil {
			return nil
		}
		for k, v := range fields {
			if v == "" {
				continue
			}
			if err := mb.Put([]byte(k), []byte(v)); err != nil {
				return err
			}
		}
		return nil
	})
}

// GetSiteCopy returns the configured copy (empty fields = defaults in use).
func (s *Store) GetSiteCopy() (SiteCopy, error) {
	var sc SiteCopy
	err := s.db.View(func(tx *bolt.Tx) error {
		mb := tx.Bucket(bMeta)
		if mb == nil {
			return nil
		}
		get := func(k string) string {
			if v := mb.Get([]byte(k)); v != nil {
				return string(v)
			}
			return ""
		}
		sc = SiteCopy{
			PortalTaglineZh: get("site_portal_tagline_zh"),
			PortalTaglineEn: get("site_portal_tagline_en"),
			PortalTitleZh:   get("site_portal_title_zh"),
			PortalTitleEn:   get("site_portal_title_en"),
			PanelTitleZh:    get("site_panel_title_zh"),
			PanelTitleEn:    get("site_panel_title_en"),
		}
		return nil
	})
	return sc, err
}

// SiteCopyWithLang picks the zh/en variant per field; the other language's
// value is ignored (the caller falls back to built-in defaults when the
// picked variant is empty).
func (sc SiteCopy) WithLang(lang string) SiteCopy {
	if strings.HasPrefix(lang, "zh") {
		return SiteCopy{PortalTaglineZh: sc.PortalTaglineZh, PortalTitleZh: sc.PortalTitleZh, PanelTitleZh: sc.PanelTitleZh}
	}
	return SiteCopy{PortalTaglineEn: sc.PortalTaglineEn, PortalTitleEn: sc.PortalTitleEn, PanelTitleEn: sc.PanelTitleEn}
}

// MarshalJSON-ish helper for the public endpoint (omit empties).
func (sc SiteCopy) PublicMap() map[string]string {
	out := map[string]string{}
	b, _ := json.Marshal(sc)
	var m map[string]string
	if json.Unmarshal(b, &m) == nil {
		for k, v := range m {
			if v != "" {
				out[k] = v
			}
		}
	}
	return out
}
