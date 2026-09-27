# A deploy whose compose.yml names another project holds, with that name: the
# copy Mission Control offers (docs/plans/copy-project.md).
class AddProposedNameToDeploys < ActiveRecord::Migration[8.1]
  def change
    add_column :deploys, :proposed_name, :string
  end
end
