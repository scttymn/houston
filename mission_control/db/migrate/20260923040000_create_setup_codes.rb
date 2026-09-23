class CreateSetupCodes < ActiveRecord::Migration[8.1]
  def change
    create_table :setup_codes do |t|
      t.string :code_digest, null: false
      t.timestamps
    end
  end
end
