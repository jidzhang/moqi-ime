package rime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWeaselABGRColorConversion(t *testing.T) {
	// 小狼毫 aqua: hilited_candidate_back_color: 0xfa3a0a → BGR 蓝 #0a3afa，不是 RGB 橙 #fa3a0a
	got := weaselIntColorToHex(0xfa3a0a, weaselColorFormatABGR)
	if got != "#0a3afa" {
		t.Fatalf("expected #0a3afa, got %q", got)
	}
	if weaselIntColorToHex(0x000000, weaselColorFormatABGR) != "#000000" {
		t.Fatalf("expected black")
	}
	if weaselIntColorToHex(0xffffff, weaselColorFormatABGR) != "#ffffff" {
		t.Fatalf("expected white")
	}
}

func TestParseWeaselAppearanceYAML(t *testing.T) {
	data := []byte(`
style:
  color_scheme: aqua
  font_face: Microsoft YaHei
  font_point: 16
  comment_font_face: Consolas
  comment_font_point: 14
  inline_preedit: true
  horizontal: true
  layout:
    candidate_spacing: 20
preset_color_schemes:
  aqua:
    back_color: 0xeceeee
    hilited_candidate_back_color: 0xfa3a0a
    candidate_text_color: 0x000000
    hilited_candidate_text_color: 0xffffff
    comment_text_color: 0x333333
    hilited_comment_text_color: 0xffffff
`)
	cfg, err := parseWeaselAppearanceYAML(data)
	if err != nil {
		t.Fatalf("parseWeaselAppearanceYAML failed: %v", err)
	}
	if cfg.CandidateTheme == nil || *cfg.CandidateTheme != "custom" {
		t.Fatalf("expected custom theme, got %#v", cfg.CandidateTheme)
	}
	if cfg.FontFace == nil || *cfg.FontFace != "Microsoft YaHei" {
		t.Fatalf("unexpected font face: %#v", cfg.FontFace)
	}
	if cfg.FontPoint == nil || *cfg.FontPoint != 16 {
		t.Fatalf("unexpected font point: %#v", cfg.FontPoint)
	}
	if cfg.CandidateCommentFontFace == nil || *cfg.CandidateCommentFontFace != "Consolas" {
		t.Fatalf("unexpected comment font face: %#v", cfg.CandidateCommentFontFace)
	}
	if cfg.InlinePreedit == nil || !*cfg.InlinePreedit {
		t.Fatalf("expected inline preedit enabled, got %#v", cfg.InlinePreedit)
	}
	if cfg.CandidatePerRow == nil || *cfg.CandidatePerRow != 9 {
		t.Fatalf("expected horizontal per row 9, got %#v", cfg.CandidatePerRow)
	}
	if cfg.CandidateSpacing == nil || *cfg.CandidateSpacing != 20 {
		t.Fatalf("unexpected candidate spacing: %#v", cfg.CandidateSpacing)
	}
	if cfg.CandidateBackgroundColor == nil {
		t.Fatalf("unexpected background color: nil")
	}
	if got := *cfg.CandidateBackgroundColor; got != "#eeeeec" {
		t.Fatalf("unexpected background color: %q", got)
	}
	if cfg.CandidateHighlightColor == nil || *cfg.CandidateHighlightColor != "#0a3afa" {
		t.Fatalf("unexpected highlight color (want aqua blue #0a3afa): %#v", cfg.CandidateHighlightColor)
	}
	if cfg.CandidateTextColor == nil || *cfg.CandidateTextColor != "#000000" {
		t.Fatalf("unexpected text color: %#v", cfg.CandidateTextColor)
	}
	if cfg.CandidateHighlightTextColor == nil || *cfg.CandidateHighlightTextColor != "#ffffff" {
		t.Fatalf("unexpected highlight text color: %#v", cfg.CandidateHighlightTextColor)
	}
}

func TestParseInstalledWeaselYAML(t *testing.T) {
	const path = `D:\Program Files\Rime\weasel-0.17.4\data\weasel.yaml`
	if _, err := os.Stat(path); err != nil {
		t.Skipf("installed weasel.yaml not found: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read weasel.yaml: %v", err)
	}
	cfg, err := parseAppearanceSkinFile(path)
	if err != nil {
		t.Fatalf("parseAppearanceSkinFile failed: %v", err)
	}
	// Also verify raw bytes path (same file).
	if _, err := parseWeaselAppearanceYAML(data); err != nil {
		t.Fatalf("parseWeaselAppearanceYAML failed: %v", err)
	}
	if cfg.FontFace == nil || *cfg.FontFace != "Microsoft YaHei" {
		t.Fatalf("unexpected font face: %#v", cfg.FontFace)
	}
	if cfg.FontPoint == nil || *cfg.FontPoint != 14 {
		t.Fatalf("unexpected font point: %#v", cfg.FontPoint)
	}
	if cfg.CandidateCommentFontFace == nil || *cfg.CandidateCommentFontFace != "Microsoft YaHei" {
		t.Fatalf("unexpected comment font face: %#v", cfg.CandidateCommentFontFace)
	}
	if cfg.CandidateCommentFontPoint == nil || *cfg.CandidateCommentFontPoint != 14 {
		t.Fatalf("unexpected comment font point: %#v", cfg.CandidateCommentFontPoint)
	}
	if cfg.InlinePreedit == nil || *cfg.InlinePreedit {
		t.Fatalf("expected inline preedit disabled for default aqua, got %#v", cfg.InlinePreedit)
	}
	if cfg.CandidatePerRow == nil || *cfg.CandidatePerRow != 1 {
		t.Fatalf("expected vertical layout per row 1, got %#v", cfg.CandidatePerRow)
	}
	if cfg.CandidateSpacing == nil || *cfg.CandidateSpacing != 5 {
		t.Fatalf("expected candidate spacing 5, got %#v", cfg.CandidateSpacing)
	}
	if cfg.CandidateBackgroundColor == nil {
		t.Fatalf("unexpected aqua background: nil")
	}
	if got := *cfg.CandidateBackgroundColor; got != "#eeeeec" {
		t.Fatalf("unexpected aqua background: %q", got)
	}
	if cfg.CandidateHighlightColor == nil || *cfg.CandidateHighlightColor != "#0a3afa" {
		t.Fatalf("unexpected aqua highlight (want blue #0a3afa): %#v", cfg.CandidateHighlightColor)
	}
	if cfg.CandidateTextColor == nil || *cfg.CandidateTextColor != "#000000" {
		t.Fatalf("unexpected aqua text color: %#v", cfg.CandidateTextColor)
	}
	if cfg.CandidateHighlightTextColor == nil || *cfg.CandidateHighlightTextColor != "#ffffff" {
		t.Fatalf("unexpected aqua highlight text: %#v", cfg.CandidateHighlightTextColor)
	}
}

func TestParseWeaselCustomPatchYAML(t *testing.T) {
	data := []byte(`
patch:
  style/color_scheme: google
  style/font_point: 18
  preset_color_schemes/google:
    back_color: 0xffffff
    hilited_candidate_back_color: 0xce7539
    candidate_text_color: 0x000000
    hilited_candidate_text_color: 0xffffff
`)
	cfg, err := parseWeaselAppearanceYAML(data)
	if err != nil {
		t.Fatalf("parseWeaselAppearanceYAML failed: %v", err)
	}
	if cfg.FontPoint == nil || *cfg.FontPoint != 18 {
		t.Fatalf("unexpected font point: %#v", cfg.FontPoint)
	}
	if cfg.CandidateBackgroundColor == nil || *cfg.CandidateBackgroundColor != "#ffffff" {
		t.Fatalf("unexpected background color: %#v", cfg.CandidateBackgroundColor)
	}
	// 小狼毫整数色按 Windows COLORREF（0xBBGGRR）解码：0xce7539 → #3975ce
	if cfg.CandidateHighlightColor == nil || *cfg.CandidateHighlightColor != "#3975ce" {
		t.Fatalf("unexpected highlight color: %#v", cfg.CandidateHighlightColor)
	}
}

func TestImportAppearanceSkinFromMoqiJSON(t *testing.T) {
	resetSharedAppearanceConfigForTest()
	ime := newTestIME()
	dir := t.TempDir()
	path := filepath.Join(dir, "appearance_config.json")
	content := `{
  "candidate_theme": "custom",
  "font_face": "SimSun",
  "font_point": 20,
  "candidate_background_color": "#fff7e8",
  "candidate_highlight_color": "#c6ddf9",
  "candidate_text_color": "#333333",
  "candidate_highlight_text_color": "#000000"
}`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write test config: %v", err)
	}
	if err := ime.importAppearanceSkinFromFile(path); err != nil {
		t.Fatalf("importAppearanceSkinFromFile failed: %v", err)
	}
	if ime.style.FontFace != "SimSun" {
		t.Fatalf("expected SimSun font, got %q", ime.style.FontFace)
	}
	if ime.style.FontPoint != 20 {
		t.Fatalf("expected font point 20, got %d", ime.style.FontPoint)
	}
	if !strings.EqualFold(ime.style.CandidateBackgroundColor, "#fff7e8") {
		t.Fatalf("unexpected background color %q", ime.style.CandidateBackgroundColor)
	}
}

func TestAppearanceMenuContainsImportSkinItem(t *testing.T) {
	ime := newTestIME()
	items := ime.buildMenu()
	appearanceMenu := findMenuByText(items, "外观(&A)")
	if appearanceMenu == nil {
		t.Fatalf("expected appearance menu")
	}
	submenu, ok := appearanceMenu["submenu"].([]map[string]interface{})
	if !ok {
		t.Fatalf("expected appearance submenu")
	}
	importItem := findMenuItemByID(submenu, ID_APPEARANCE_IMPORT_SKIN)
	if importItem == nil {
		t.Fatalf("expected import skin menu item, submenu=%#v", submenu)
	}
	if importItem["text"] != "导入皮肤" {
		t.Fatalf("unexpected menu text: %#v", importItem["text"])
	}
}

func findMenuByText(items []map[string]interface{}, text string) map[string]interface{} {
	for _, item := range items {
		if item["text"] == text {
			return item
		}
	}
	return nil
}

func findMenuItemByID(items []map[string]interface{}, id int) map[string]interface{} {
	for _, item := range items {
		if itemID, ok := item["id"].(int); ok && itemID == id {
			return item
		}
		if submenu, ok := item["submenu"].([]map[string]interface{}); ok {
			if found := findMenuItemByID(submenu, id); found != nil {
				return found
			}
		}
	}
	return nil
}
