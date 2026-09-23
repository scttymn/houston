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

ActiveRecord::Schema[8.1].define(version: 2026_09_23_060000) do
  create_table "installations", force: :cascade do |t|
    t.string "base_domain"
    t.string "cloudflare_account_id"
    t.text "cloudflare_api_token"
    t.datetime "cloudflare_connected_at"
    t.string "cloudflare_zone_id"
    t.datetime "created_at", null: false
    t.string "tunnel_id"
    t.text "tunnel_token"
    t.datetime "updated_at", null: false
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

  add_foreign_key "sessions", "users"
end
