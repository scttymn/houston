# A failed copy's deploy log, kept: its new project, and the log with it, is
# removed (docs/plans/copy-project.md).
class AddLogToProjectCopies < ActiveRecord::Migration[8.1]
  def change
    add_column :project_copies, :log, :text
  end
end
