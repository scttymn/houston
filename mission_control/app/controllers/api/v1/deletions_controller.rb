class Api::V1::DeletionsController < Api::V1::BaseController
  # A deletion, by its id (houston delete --follow): it outlives its project.
  def show
    deletion = ProjectDeletion.find_by(id: params[:id])
    return render json: { error: "no deletion #{params[:id]}" }, status: :not_found unless deletion
    render json: RemoteView.deletion(deletion)
  end
end
