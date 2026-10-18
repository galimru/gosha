package notion

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRender(t *testing.T) {
	var blocks []Block
	if err := json.Unmarshal([]byte(renderFixture), &blocks); err != nil {
		t.Fatal(err)
	}
	// Nested children, as Mirror.blocks fills them in.
	blocks[2].Children = []Block{blocks[3]}
	blocks = append(blocks[:3], blocks[4:]...)
	r := render(blocks)
	want := strings.TrimSpace(renderWant) + "\n"
	if r.Body != want {
		t.Errorf("body:\n%s\nwant:\n%s", r.Body, want)
	}
	if len(r.Children) != 1 || r.Children[0] != "1a2b3c4d5e6f708192a3b4c5d6e7f809" {
		t.Errorf("children = %v", r.Children)
	}
}

func TestResolve(t *testing.T) {
	body := "[Old](notion-page:1a2b3c4d5e6f708192a3b4c5d6e7f809#title) and [here](notion-page:ffffffffffffffffffffffffffffffff)"
	got := resolve(body, func(id string) (string, string, bool) {
		if id == "1a2b3c4d5e6f708192a3b4c5d6e7f809" {
			return "../a.md", "New [x]", true
		}
		return "", "", false
	})
	want := `[New \[x\]](../a.md) and [here](https://www.notion.so/ffffffffffffffffffffffffffffffff)`
	if got != want {
		t.Errorf("got %s", got)
	}
}

func TestParsePageID(t *testing.T) {
	const want = "1a2b3c4d5e6f708192a3b4c5d6e7f809"
	for _, in := range []string{
		"https://www.notion.so/NextDesk-1a2b3c4d5e6f708192a3b4c5d6e7f809",
		"https://www.notion.so/team/Cafe-1a2b3c4d5e6f708192a3b4c5d6e7f809?pvs=4",
		"1a2b3c4d-5e6f-7081-92a3-b4c5d6e7f809",
		"1A2B3C4D5E6F708192A3B4C5D6E7F809",
	} {
		if got, err := ParsePageID(in); err != nil || got != want {
			t.Errorf("ParsePageID(%q) = %q, %v", in, got, err)
		}
	}
	if _, err := ParsePageID("https://www.notion.so/NextDesk"); err == nil {
		t.Error("no id: want error")
	}
}

const renderFixture = `[
 {"id":"h","type":"heading_2","heading_2":{"rich_text":[{"type":"text","plain_text":"Auth"}]}},
 {"id":"p","type":"paragraph","paragraph":{"rich_text":[
   {"type":"text","plain_text":"Use "},
   {"type":"text","plain_text":"OAuth ","annotations":{"bold":true}},
   {"type":"text","plain_text":"tokens","annotations":{"code":true}},
   {"type":"text","plain_text":", see ","annotations":{}},
   {"type":"text","plain_text":"docs","href":"https://example.com/x"},
   {"type":"text","plain_text":" and ","annotations":{}},
   {"type":"mention","plain_text":"Data model","mention":{"type":"page","page":{"id":"1a2b3c4d-5e6f-7081-92a3-b4c5d6e7f800"}}},
   {"type":"text","plain_text":"."}]}},
 {"id":"l1","type":"bulleted_list_item","has_children":true,"bulleted_list_item":{"rich_text":[{"type":"text","plain_text":"one"}]}},
 {"id":"l1a","type":"to_do","to_do":{"rich_text":[{"type":"text","plain_text":"done"}],"checked":true}},
 {"id":"l2","type":"bulleted_list_item","bulleted_list_item":{"rich_text":[{"type":"text","plain_text":"two"}]}},
 {"id":"c","type":"code","code":{"rich_text":[{"type":"text","plain_text":"go test ./..."}],"language":"bash"}},
 {"id":"q","type":"callout","callout":{"rich_text":[{"type":"text","plain_text":"Careful"}],"icon":{"type":"emoji","emoji":"⚠️"}}},
 {"id":"1a2b3c4d-5e6f-7081-92a3-b4c5d6e7f809","type":"child_page","child_page":{"title":"Sub"}},
 {"id":"d","type":"child_database","child_database":{"title":"Tasks"}},
 {"id":"i","type":"image","image":{"type":"file","file":{"url":"https://s3/x?sig"},"caption":[{"type":"text","plain_text":"Diagram"}]}},
 {"id":"x","type":"ai_block","ai_block":{}},
 {"id":"dv","type":"divider","divider":{}}
]`

const renderWant = "## Auth\n\n" +
	"Use **OAuth** `tokens`, see [docs](https://example.com/x) and [Data model](notion-page:1a2b3c4d5e6f708192a3b4c5d6e7f800#title).\n\n" +
	"- one\n  - [x] done\n- two\n\n" +
	"```bash\ngo test ./...\n```\n\n" +
	"> ⚠️ Careful\n\n" +
	"[Sub](notion-page:1a2b3c4d5e6f708192a3b4c5d6e7f809#title)\n\n" +
	"<!-- database \"Tasks\" not mirrored -->\n\n" +
	"image: Diagram <!-- hosted in Notion, not mirrored -->\n\n" +
	"<!-- unsupported: ai_block -->\n\n" +
	"---"
