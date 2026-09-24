# Add project shows the webhook secret before Save (the design's step 04),
# so the draft makes it.
class AddWebhookSecretToRepoLinks < ActiveRecord::Migration[8.1]
  def change
    add_column :repo_links, :webhook_secret, :text
  end
end
