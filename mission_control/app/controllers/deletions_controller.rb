# A deletion's page: its steps and log while it runs, and afterwards what's
# left (the final snapshot) and what Houston can't remove (the git host's
# deploy key and webhook).
class DeletionsController < ApplicationController
  def show
    @deletion = ProjectDeletion.find(params[:id])
  end
end
