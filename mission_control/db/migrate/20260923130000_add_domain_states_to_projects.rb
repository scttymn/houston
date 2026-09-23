class AddDomainStatesToProjects < ActiveRecord::Migration[8.1]
  def change
    # domain → { "state" => "DNS OK" | "DNS PENDING" | "WILDCARD" | …, "reason" => … }, from the last sync
    add_column :projects, :domain_states, :json, null: false, default: {}
  end
end
