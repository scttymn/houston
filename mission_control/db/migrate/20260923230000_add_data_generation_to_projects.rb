# Build step 6, Batch 3: a project's data generation (its volumes' and
# accessories' names). Every existing project is generation 1: today's names.
class AddDataGenerationToProjects < ActiveRecord::Migration[8.1]
  def change
    add_column :projects, :data_generation, :integer, default: 1, null: false
  end
end
