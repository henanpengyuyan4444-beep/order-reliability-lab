# AI-assisted learning exercise, NOT yet executed in the delivery environment.
# In-memory only: supervision restarts a process, not its business data.
ExUnit.start()

defmodule LabReviewWorker do
  use GenServer

  def start_link(_), do: GenServer.start_link(__MODULE__, %{}, name: __MODULE__)
  def add(id), do: GenServer.call(__MODULE__, {:add, id})
  def list(), do: GenServer.call(__MODULE__, :list)

  @impl true
  def init(state), do: {:ok, state}

  @impl true
  def handle_call({:add, id}, _from, state) when is_binary(id) do
    if Map.has_key?(state, id) do
      {:reply, :already_present, state}
    else
      {:reply, :queued, Map.put(state, id, :needs_review)}
    end
  end

  def handle_call(:list, _from, state), do: {:reply, state, state}
end

defmodule LabReviewWorkerTest do
  use ExUnit.Case

  test "duplicate business ID creates one review item" do
    start_supervised!(LabReviewWorker)
    assert LabReviewWorker.add("synthetic-order-1") == :queued
    assert LabReviewWorker.add("synthetic-order-1") == :already_present
    assert LabReviewWorker.list() == %{"synthetic-order-1" => :needs_review}
  end
end

# Learner task: add complete(id) with tests, then explore Supervisor restart
# in iex. Explain why killing this process loses its map despite a restart.
