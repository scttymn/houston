# Secrets from the CLI: write-only, like the project page. Names and set or
# not come back; a value never does.
class Api::V1::SecretsController < Api::V1::BaseController
  MAX_BODY = 64.kilobytes

  before_action :set_project
  before_action :set_key, except: :index

  def index
    have = @project.secrets.index_by(&:key)
    render json: { secrets: @project.variables.map { |v|
      secret = have[v["name"]]
      { name: v["name"], required: v["required"] == true, set: secret&.value.present? || false, updated_at: secret&.updated_at }
    } }
  end

  def update
    body = request.body.read(MAX_BODY + 1).to_s
    return render json: { error: "a value can be at most #{MAX_BODY / 1.kilobyte} KiB" }, status: :content_too_large if body.bytesize > MAX_BODY
    value = (JSON.parse(body)["value"] rescue nil)
    save(value)
  end

  def generate
    save(Secret.generated_value)
  end

  def destroy
    @project.secrets.where(key: @key).delete_all
    render json: { name: @key, set: false }
  end

  private
    def save(value)
      secret = @project.secrets.find_or_initialize_by(key: @key)
      secret.value = value
      if secret.save
        render json: { name: @key, set: true }
      else
        render json: { error: "#{@key} #{secret.errors[:value].to_sentence}" }, status: :unprocessable_entity
      end
    end

    def set_project
      @project = Project.find_by(name: params[:project_name])
      render json: { error: "no project #{params[:project_name]}" }, status: :not_found unless @project
    end

    def set_key
      @key = params[:key]
      render json: { error: "#{@project.name}'s compose.yml doesn't reference #{@key}" }, status: :not_found unless @project.variable?(@key)
    end
end
