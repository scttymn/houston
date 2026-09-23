# A project's Docker names in one data generation, matching Go's kamal.Names:
# generation 1 is the names every project had before generations; a restore
# builds generation g+1 beside the live one (docs/plans/restore.md).
class Generation
  attr_reader :number

  def initialize(project, number)
    @project = project
    @number = number
  end

  # <name>_<volume>, or <name>.g<g>_<volume> (project names have no dots).
  def volume(name) = number <= 1 ? "#{@project.name}_#{name}" : "#{@project.name}.g#{number}_#{name}"

  # The accessory's container, and its <SERVICE>_HOST: <name>-<service>[-g<g>].
  def container(service) = number <= 1 ? "#{@project.name}-#{service}" : "#{@project.name}-#{service}-g#{number}"

  # Where a volume's directory lives inside its storage location.
  def directory(name) = number <= 1 ? "volumes/#{@project.name}/#{name}" : "volumes/#{@project.name}.g#{number}/#{name}"
end
