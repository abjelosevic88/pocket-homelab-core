package server

import "testing"

func TestMCPOpKind(t *testing.T) {
	kinds := map[string]string{}
	for _, op := range mcpAppCatalog {
		kinds[op.Name] = op.Kind
	}
	for name, want := range map[string]string{
		"pocket_projects_create_issue": "add", "pocket_projects_link_pr": "add", "pocket_projects_update_issue": "change",
		"pocket_projects_issues": "read", "pocket_files_save_text": "change", "pocket_tasks_complete": "change", "pocket_notes_append": "add",
		"pocket_weather_summary": "read", "pocket_weather_add_place": "change",
		"pocket_fitness_summary": "read", "pocket_fitness_log_workout": "change",
		"pocket_chat_summary": "read", "pocket_chat_messages": "read",
		"pocket_crm_summary": "read", "pocket_crm_deals": "read", "pocket_crm_add_lead": "add", "pocket_crm_add_note": "add",
		"pocket_forms_summary": "read", "pocket_forms_create_form": "add", "pocket_forms_open_form": "change", "pocket_forms_close_form": "change",
		"pocket_blog_summary": "read", "pocket_blog_create_draft": "add", "pocket_blog_update_post": "change", "pocket_blog_publish": "change", "pocket_blog_reply_comment": "change",
		"pocket_nutrition_summary": "read", "pocket_nutrition_foods_search": "read", "pocket_nutrition_log_food": "change", "pocket_nutrition_add_water": "change",
	} {
		if kinds[name] != want {
			t.Errorf("%s: kind %q, want %q", name, kinds[name], want)
		}
	}
	for name := range mcpChangePosts {
		if _, ok := kinds["pocket_"+name]; !ok {
			t.Errorf("mcpChangePosts names a missing operation %s", name)
		}
	}
}
