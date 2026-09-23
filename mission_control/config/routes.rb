Rails.application.routes.draw do
  resource :session, only: %i[ new create destroy ]
  resource :setup, only: %i[ show create ], controller: "setup"
  namespace :setup do
    resource :cloudflare, only: %i[ show create ], controller: "cloudflare"
    resource :storage, only: %i[ show create ], controller: "storage" do
      post :finish
      get :password
    end
  end

  # Reveal health status on /up that returns 200 if the app boots with no exceptions, otherwise 500.
  # Can be used by load balancers and uptime monitors to verify that the app is live.
  get "up" => "rails/health#show", as: :rails_health_check
  get "ping" => "pings#show"

  namespace :api do
    post "projects/sync", to: "projects#sync"
    get "projects/:name/secrets/:key", to: "secrets#show", constraints: { key: %r{[^/]+} }
    post "projects/:name/deploys", to: "deploys#create"
    patch "deploys/:id", to: "deploys#update"
  end

  resources :projects, only: :show, param: :name do
    resources :deploys, only: :show, param: :number
    resources :secrets, only: %i[ update destroy ], param: :key do
      post :generate, on: :member
    end
  end

  root "projects#index"
end
