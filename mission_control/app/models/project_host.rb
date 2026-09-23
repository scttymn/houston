# One container-name prefix a project owns. The unique index on name is what
# keeps two projects from sharing one (see Project#host_names).
class ProjectHost < ApplicationRecord
  belongs_to :project
end
