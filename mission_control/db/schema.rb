# This file is auto-generated from the current state of the database. Instead
# of editing this file, please use the migrations feature of Active Record to
# incrementally modify your database, and then regenerate this schema definition.
#
# This file is the source Rails uses to define your schema when running `bin/rails
# db:schema:load`. When creating a new database, `bin/rails db:schema:load` tends to
# be faster and is potentially less error prone than running all of your
# migrations from scratch. Old migrations may fail to apply correctly if those
# migrations use external dependencies or application code.
#
# It's strongly recommended that you check this file into your version control system.

ActiveRecord::Schema[8.1].define(version: 2026_09_23_130000) do
  create_table "deploys", force: :cascade do |t|
    t.datetime "created_at", null: false
    t.string "error"
    t.datetime "finished_at"
    t.datetime "heartbeat_at", null: false
    t.text "log", default: "", null: false
    t.integer "number", null: false
    t.integer "project_id", null: false
    t.string "ref", null: false
    t.string "runner"
    t.string "sha", null: false
    t.string "status", default: "in_flight", null: false
    t.string "step"
    t.string "token_digest", null: false
    t.datetime "updated_at", null: false
    t.index ["project_id", "number"], name: "index_deploys_on_project_id_and_number", unique: true
    t.index ["project_id"], name: "index_deploys_on_project_id"
    t.index ["project_id"], name: "index_deploys_one_in_flight", unique: true, where: "status = 'in_flight'"
    t.index ["project_id"], name: "index_deploys_one_queued", unique: true, where: "status = 'queued'"
  end

  create_table "installations", force: :cascade do |t|
    t.string "base_domain"
    t.string "cloudflare_account_id"
    t.text "cloudflare_api_token"
    t.datetime "cloudflare_connected_at"
    t.string "cloudflare_zone_id"
    t.datetime "created_at", null: false
    t.string "dns_mode"
    t.string "tunnel_id"
    t.text "tunnel_token"
    t.datetime "updated_at", null: false
  end

  create_table "project_hosts", force: :cascade do |t|
    t.datetime "created_at", null: false
    t.string "name", null: false
    t.integer "project_id", null: false
    t.datetime "updated_at", null: false
    t.index ["name"], name: "index_project_hosts_on_name", unique: true
    t.index ["project_id"], name: "index_project_hosts_on_project_id"
  end

  create_table "projects", force: :cascade do |t|
    t.string "app_service", null: false
    t.string "branch"
    t.string "compose_path"
    t.datetime "created_at", null: false
    t.text "deploy_key_private"
    t.string "deploy_key_public"
    t.json "deploy_rule", default: {}, null: false
    t.json "domain_states", default: {}, null: false
    t.json "domains", default: [], null: false
    t.string "health", null: false
    t.string "last_check_error"
    t.datetime "last_checked_at"
    t.string "name", null: false
    t.integer "port", null: false
    t.string "repo_url"
    t.json "seen_refs", default: {}, null: false
    t.json "services", default: [], null: false
    t.datetime "synced_at"
    t.datetime "updated_at", null: false
    t.json "variables", default: [], null: false
    t.text "webhook_secret"
    t.datetime "webhook_verified_at"
    t.index ["name"], name: "index_projects_on_name", unique: true
  end

  create_table "repo_links", force: :cascade do |t|
    t.string "branch", default: "main", null: false
    t.string "compose_path", default: "compose.yml", null: false
    t.datetime "created_at", null: false
    t.text "deploy_key_private", null: false
    t.string "deploy_key_public", null: false
    t.json "preview"
    t.string "preview_sha"
    t.string "repo_url", null: false
    t.datetime "updated_at", null: false
  end

  create_table "runners", force: :cascade do |t|
    t.datetime "created_at", null: false
    t.datetime "last_seen_at", null: false
    t.string "name", null: false
    t.datetime "updated_at", null: false
    t.index ["name"], name: "index_runners_on_name", unique: true
  end

  create_table "secrets", force: :cascade do |t|
    t.datetime "created_at", null: false
    t.string "key", null: false
    t.integer "project_id", null: false
    t.datetime "updated_at", null: false
    t.text "value"
    t.index ["project_id", "key"], name: "index_secrets_on_project_id_and_key", unique: true
    t.index ["project_id"], name: "index_secrets_on_project_id"
  end

  create_table "sessions", force: :cascade do |t|
    t.datetime "created_at", null: false
    t.string "ip_address"
    t.datetime "updated_at", null: false
    t.string "user_agent"
    t.integer "user_id", null: false
    t.index ["user_id"], name: "index_sessions_on_user_id"
  end

  create_table "setup_codes", force: :cascade do |t|
    t.string "code_digest", null: false
    t.datetime "created_at", null: false
    t.datetime "updated_at", null: false
  end

  create_table "storage_locations", force: :cascade do |t|
    t.datetime "acknowledged_at"
    t.datetime "created_at", null: false
    t.text "credentials"
    t.boolean "default", default: false, null: false
    t.string "kind", null: false
    t.string "name", null: false
    t.text "restic_password"
    t.text "settings"
    t.datetime "updated_at", null: false
    t.datetime "verified_at"
    t.index ["name"], name: "index_storage_locations_on_name", unique: true
  end

  create_table "users", force: :cascade do |t|
    t.datetime "created_at", null: false
    t.string "email_address", null: false
    t.string "password_digest", null: false
    t.datetime "updated_at", null: false
    t.index ["email_address"], name: "index_users_on_email_address", unique: true
  end

  add_foreign_key "deploys", "projects", on_delete: :cascade
  add_foreign_key "project_hosts", "projects", on_delete: :cascade
  add_foreign_key "secrets", "projects", on_delete: :cascade
  add_foreign_key "sessions", "users"
end
